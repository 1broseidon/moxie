package dispatch

import (
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/1broseidon/moxie/internal/scheduler"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

var (
	dispatchMu   sync.Mutex
	shuttingDown atomic.Bool
)

type Callbacks struct {
	OnActivity    func(activity string)
	OnResult      func(result string) error
	OnStatusClear func()
	OnDone        func()
}

func SetShuttingDown(v bool) {
	shuttingDown.Store(v)
}

func IsShuttingDown() bool {
	return shuttingDown.Load()
}

func RunModel(job *store.PendingJob, client *oneagent.Client, onActivity func(activity string)) (string, bool) {
	opts := oneagent.RunOpts{
		Backend:  job.State.Backend,
		Prompt:   job.Prompt,
		Model:    job.State.Model,
		CWD:      job.CWD,
		ThreadID: job.State.ThreadID,
		Source:   "telegram",
	}

	emit := func(ev oneagent.StreamEvent) {
		if ev.Type == "activity" && ev.Activity != "" {
			log.Printf("[%s] %s", job.State.Backend, ev.Activity)
			if onActivity != nil {
				onActivity(ev.Activity)
			}
		}
	}

	resp := client.RunWithThreadStream(opts, emit)
	if resp.Error != "" {
		if IsShuttingDown() && IsShutdownError(resp.Error) {
			log.Printf("%s interrupted by shutdown: %s", job.State.Backend, resp.Error)
			return "", true
		}
		log.Printf("%s error: %s", job.State.Backend, resp.Error)
		return resp.Error, false
	}
	return resp.Result, false
}

func ProcessJob(job *store.PendingJob, client *oneagent.Client, schedules *scheduler.Store, callbacks Callbacks) {
	defer func() {
		if callbacks.OnDone != nil {
			callbacks.OnDone()
		}
	}()

	dispatchMu.Lock()
	defer dispatchMu.Unlock()

	if job.Status != "ready" && job.Status != "delivered" {
		job.Status = "running"
		store.WriteJob(*job)
		result, interrupted := RunModel(job, client, callbacks.OnActivity)
		if interrupted {
			return
		}
		job.Result = result
		job.Status = "ready"
		store.WriteJob(*job)
	}
	if callbacks.OnStatusClear != nil {
		callbacks.OnStatusClear()
	}
	if job.Status != "delivered" {
		if callbacks.OnResult != nil {
			if err := callbacks.OnResult(job.Result); err != nil {
				log.Printf("delivery error for %d: %v", job.UpdateID, err)
				store.WriteJob(*job)
				return
			}
		}
		job.Status = "delivered"
		store.WriteJob(*job)
	}
	if job.ScheduleID != "" && schedules != nil {
		if _, err := schedules.MarkDone(job.ScheduleID, job.UpdateID, time.Now()); err != nil {
			log.Printf("schedule completion error for %s: %v", job.ScheduleID, err)
			return
		}
	}
	store.CleanupJobTemp(*job)
	store.RemoveJob(job.UpdateID)
}

func CanRetryJob(job store.PendingJob) bool {
	if job.TempPath == "" {
		return true
	}
	if _, err := os.Stat(job.TempPath); err == nil {
		return true
	} else if os.IsNotExist(err) {
		log.Printf("cannot retry job %d: missing temp file %s", job.UpdateID, job.TempPath)
	} else {
		log.Printf("cannot retry job %d: temp file check failed for %s: %v", job.UpdateID, job.TempPath, err)
	}
	return false
}

func RecoverPendingJobs(client *oneagent.Client, schedules *scheduler.Store, callbackFactory func(*store.PendingJob) Callbacks) bool {
	storedJobs := store.ListJobs()
	if len(storedJobs) == 0 {
		return false
	}
	log.Printf("recovering %d pending job(s)", len(storedJobs))
	maxRecovered := 0
	for _, storedJob := range storedJobs {
		job := storedJob
		callbacks := Callbacks{}
		if callbackFactory != nil {
			callbacks = callbackFactory(&job)
		}
		switch job.Status {
		case "ready":
			log.Printf("replaying ready job %d", job.UpdateID)
			ProcessJob(&job, client, schedules, callbacks)
			if job.UpdateID > maxRecovered {
				maxRecovered = job.UpdateID
			}
		case "delivered":
			log.Printf("finalizing delivered job %d", job.UpdateID)
			ProcessJob(&job, client, schedules, callbacks)
			if job.UpdateID > maxRecovered {
				maxRecovered = job.UpdateID
			}
		case "running":
			if !CanRetryJob(job) {
				log.Printf("discarding interrupted job %d; update will be retried", job.UpdateID)
				store.CleanupJobTemp(job)
				store.RemoveJob(job.UpdateID)
				continue
			}
			log.Printf("retrying interrupted job %d", job.UpdateID)
			ProcessJob(&job, client, schedules, callbacks)
			if job.UpdateID > maxRecovered {
				maxRecovered = job.UpdateID
			}
		default:
			log.Printf("discarding unknown job state %q for %d", job.Status, job.UpdateID)
			store.CleanupJobTemp(job)
			store.RemoveJob(job.UpdateID)
		}
	}
	if maxRecovered > store.ReadCursor() {
		store.WriteCursor(maxRecovered)
	}
	return true
}

func RetryDeliverableJobs(client *oneagent.Client, schedules *scheduler.Store, callbackFactory func(*store.PendingJob) Callbacks) bool {
	storedJobs := store.ListJobs()
	if len(storedJobs) == 0 {
		return false
	}

	retried := false
	for _, storedJob := range storedJobs {
		if storedJob.Status != "ready" && storedJob.Status != "delivered" {
			continue
		}

		job := storedJob
		callbacks := Callbacks{}
		if callbackFactory != nil {
			callbacks = callbackFactory(&job)
		}
		log.Printf("retrying deliverable job %d (%s)", job.UpdateID, job.Status)
		ProcessJob(&job, client, schedules, callbacks)
		retried = true
	}
	return retried
}

func NewSyntheticJobID() int {
	n := time.Now().UnixNano()
	if n < 0 {
		n = -n
	}
	maxInt := int64(^uint(0) >> 1)
	if n > maxInt {
		n %= maxInt
	}
	if n == 0 {
		n = 1
	}
	return -int(n)
}

func IsShutdownError(errText string) bool {
	errText = strings.ToLower(errText)
	return strings.Contains(errText, "signal: terminated") ||
		strings.Contains(errText, "context canceled") ||
		strings.Contains(errText, "interrupted by signal")
}

func IsMissingNativeSessionError(errText string) bool {
	errText = strings.ToLower(errText)
	return strings.Contains(errText, "thread does not exist") ||
		strings.Contains(errText, "session not found") ||
		strings.Contains(errText, "no conversation found")
}

func ClearNativeSession(client *oneagent.Client, st store.State) bool {
	if client == nil || client.Store == nil || st.Backend == "" || st.ThreadID == "" {
		return false
	}

	thread, err := client.LoadThread(st.ThreadID)
	if err != nil || thread == nil || thread.NativeSessions == nil {
		return false
	}
	if _, ok := thread.NativeSessions[st.Backend]; !ok {
		return false
	}

	delete(thread.NativeSessions, st.Backend)
	if len(thread.NativeSessions) == 0 {
		thread.NativeSessions = nil
	}
	if err := client.SaveThread(thread); err != nil {
		log.Printf("clear native session save failed for %s/%s: %v", st.ThreadID, st.Backend, err)
		return false
	}
	return true
}
