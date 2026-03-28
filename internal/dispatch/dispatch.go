package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/1broseidon/moxie/internal/scheduler"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

var (
	defaultDispatchMu sync.Mutex
	dispatchLocks     sync.Map
	shuttingDown      atomic.Bool
	runModelFunc      = RunModel
)

func SetRunModelFuncForTest(fn func(*store.PendingJob, *oneagent.Client, func(string)) (string, bool)) func() {
	prev := runModelFunc
	if fn == nil {
		runModelFunc = RunModel
	} else {
		runModelFunc = fn
	}
	return func() {
		runModelFunc = prev
	}
}

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
	maybeClearStalePiSession(client, job)

	resp := runBackend(job, client, onActivity)
	if resp.Error != "" && job != nil && job.State.Backend == "pi" && IsMissingNativeSessionError(resp.Error) {
		if ClearNativeSession(client, job.State) {
			log.Printf("cleared missing pi native session for %s; retrying once", job.State.ThreadID)
			resp = runBackend(job, client, onActivity)
		}
	}

	if resp.Error != "" {
		if IsShuttingDown() && IsShutdownError(resp.Error) {
			log.Printf("%s interrupted by shutdown: %s", job.State.Backend, resp.Error)
			return "", true
		}
		log.Printf("%s error: %s", job.State.Backend, resp.Error)
		return resp.Error, false
	}
	return normalizeModelResult(resp.Result), false
}

func runBackend(job *store.PendingJob, client *oneagent.Client, onActivity func(activity string)) oneagent.Response {
	emit := func(ev oneagent.StreamEvent) {
		if ev.Type == "activity" && ev.Activity != "" {
			log.Printf("[%s] %s", job.State.Backend, ev.Activity)
			if onActivity != nil {
				onActivity(ev.Activity)
			}
		}
	}
	return runBackendWithContext(context.Background(), job, client, emit)
}

func runBackendWithContext(ctx context.Context, job *store.PendingJob, client *oneagent.Client, emit func(oneagent.StreamEvent)) oneagent.Response {
	if job == nil {
		return oneagent.Response{Error: "missing job"}
	}
	if client == nil {
		return oneagent.Response{Error: "missing client", Backend: job.State.Backend, ThreadID: job.State.ThreadID}
	}

	opts := oneagent.RunOpts{
		Backend:  job.State.Backend,
		Prompt:   job.Prompt,
		Model:    job.State.Model,
		Thinking: job.State.Thinking,
		CWD:      job.CWD,
		ThreadID: job.State.ThreadID,
		Source:   job.Source,
	}
	runClient := clientWithJobEnv(client, job)
	if ctx == nil {
		ctx = context.Background()
	}
	return runClient.RunStreamContext(ctx, opts, emit)
}

func clientWithJobEnv(client *oneagent.Client, job *store.PendingJob) *oneagent.Client {
	if client == nil || job == nil || strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.State.Backend) == "" {
		return client
	}
	backend, ok := client.Backends[job.State.Backend]
	if !ok {
		return client
	}
	wrapped, ok := wrapBackendWithJobEnv(backend, job.ID)
	if !ok {
		return client
	}
	cloned := *client
	cloned.Backends = make(map[string]oneagent.Backend, len(client.Backends))
	for name, candidate := range client.Backends {
		cloned.Backends[name] = candidate
	}
	cloned.Backends[job.State.Backend] = wrapped
	return &cloned
}

func wrapBackendWithJobEnv(backend oneagent.Backend, jobID string) (oneagent.Backend, bool) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || runtime.GOOS == "windows" {
		return backend, false
	}
	backend.Cmd = wrapCommandWithEnv(backend.Cmd, "MOXIE_JOB_ID", jobID)
	backend.ResumeCmd = wrapCommandWithEnv(backend.ResumeCmd, "MOXIE_JOB_ID", jobID)
	return backend, len(backend.Cmd) > 0
}

func wrapCommandWithEnv(args []string, key, value string) []string {
	if len(args) == 0 {
		return nil
	}
	wrapped := make([]string, 0, len(args)+2)
	wrapped = append(wrapped, "env", key+"="+value)
	wrapped = append(wrapped, args...)
	return wrapped
}

func maybeClearStalePiSession(client *oneagent.Client, job *store.PendingJob) bool {
	if client == nil || job == nil || job.State.Backend != "pi" || job.State.ThreadID == "" {
		return false
	}

	thread, err := client.LoadThread(job.State.ThreadID)
	if err != nil || thread == nil || thread.NativeSessions == nil {
		return false
	}
	sessionID := strings.TrimSpace(thread.NativeSessions["pi"])
	if sessionID == "" || piSessionExists(job.CWD, sessionID) {
		return false
	}

	if ClearNativeSession(client, job.State) {
		log.Printf("cleared stale pi native session %s for %s", sessionID, job.State.ThreadID)
		return true
	}
	return false
}

func piSessionExists(cwd, sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}

	dir := piSessionDir(cwd)
	if dir == "" {
		return false
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*_"+sessionID+".jsonl"))
	return err == nil && len(matches) > 0
}

func piSessionDir(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return ""
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	clean := filepath.Clean(cwd)
	clean = strings.Trim(clean, string(filepath.Separator))
	if clean == "" {
		clean = "root"
	}

	name := "--" + strings.ReplaceAll(clean, string(filepath.Separator), "-") + "--"
	return filepath.Join(home, ".pi", "agent", "sessions", name)
}

// preflightJob checks whether the backend CLI for a job is available and healthy.
// Skipped when the client has no backends loaded (e.g. in tests where RunModel is mocked).
func preflightJob(client *oneagent.Client, job *store.PendingJob) error {
	if client == nil || job == nil || len(client.Backends) == 0 {
		return nil
	}
	return client.PreflightCheck(job.State.Backend)
}

func normalizeModelResult(result string) string {
	switch strings.TrimSpace(result) {
	case "", "Done — nothing to report.", "Done - nothing to report.":
		return ""
	default:
		return result
	}
}

func conversationLock(conversationID string) *sync.Mutex {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return &defaultDispatchMu
	}
	lock, _ := dispatchLocks.LoadOrStore(conversationID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func jobLockKey(job *store.PendingJob) string {
	if job == nil {
		return ""
	}
	if threadID := strings.TrimSpace(job.State.ThreadID); threadID != "" {
		return "thread:" + threadID
	}
	return "conversation:" + strings.TrimSpace(job.ConversationID)
}

func jobLock(job *store.PendingJob) *sync.Mutex {
	key := jobLockKey(job)
	if key == "" {
		return &defaultDispatchMu
	}
	lock, _ := dispatchLocks.LoadOrStore(key, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func IsSubagentJob(job *store.PendingJob) bool {
	return job != nil && IsSubagentSource(job.Source)
}

func IsSubagentSource(source string) bool {
	return source == "subagent" || source == "subagent-synthesis"
}

func processJob(job *store.PendingJob, client *oneagent.Client, schedules *scheduler.Store, callbacks Callbacks) {
	defer func() {
		if callbacks.OnDone != nil {
			callbacks.OnDone()
		}
	}()

	if job.Source == "exec" {
		processExecJob(job, schedules, callbacks)
		return
	}

	// Preflight: verify the backend CLI is present and healthy before
	// spending time on the job. Delivers the error instantly instead of
	// retrying a doomed invocation through the supervisor loop.
	if client != nil && job.Status != "ready" && job.Status != "delivered" {
		if err := preflightJob(client, job); err != nil {
			log.Printf("preflight failed for %s (%s): %v", job.ID, job.State.Backend, err)
			job.Result = fmt.Sprintf("preflight error: %v", err)
			job.Status = "ready"
			store.WriteJob(*job)
			if callbacks.OnStatusClear != nil {
				callbacks.OnStatusClear()
			}
			deliverAndFinalize(job, schedules, callbacks)
			return
		}
	}

	if job.Status != "ready" && job.Status != "delivered" {
		job.Status = "running"
		store.WriteJob(*job)

		var (
			result      string
			interrupted bool
		)
		if isSupervisedSubagentJob(job, client) {
			result, interrupted = runSupervisedSubagent(job, client, callbacks.OnActivity)
		} else {
			result, interrupted = runModelFunc(job, client, callbacks.OnActivity)
		}
		if interrupted {
			if finalizeBlockingSubagentFailure(job, blockingSubagentInterruptReason()) {
				return
			}
			return
		}
		job.Result = result
		job.Status = "ready"
		store.WriteJob(*job)
	}
	if callbacks.OnStatusClear != nil {
		callbacks.OnStatusClear()
	}
	deliverAndFinalize(job, schedules, callbacks)
}

func processExecJob(job *store.PendingJob, schedules *scheduler.Store, callbacks Callbacks) {
	if job.Status != "ready" && job.Status != "delivered" {
		job.Status = "running"
		store.WriteJob(*job)
		result, err := runExecJob(job)
		if err != nil {
			log.Printf("exec job %s failed: %v", job.ID, err)
			finalizeSchedule(job, schedules)
			store.RemoveJob(job.ID)
			return
		}
		if strings.TrimSpace(result) == "" {
			log.Printf("exec job %s produced no output — skipping delivery", job.ID)
			finalizeSchedule(job, schedules)
			store.RemoveJob(job.ID)
			return
		}
		job.Result = result
		job.Status = "ready"
		store.WriteJob(*job)
	}
	deliverAndFinalize(job, schedules, callbacks)
}

func deliverAndFinalize(job *store.PendingJob, schedules *scheduler.Store, callbacks Callbacks) {
	if job.Status != "delivered" {
		if callbacks.OnResult != nil {
			if err := callbacks.OnResult(job.Result); err != nil {
				log.Printf("delivery error for %s: %v", job.ID, err)
				store.WriteJob(*job)
				return
			}
		}
		job.Status = "delivered"
		store.WriteJob(*job)
	}
	finalizeSchedule(job, schedules)
	writeArtifact(job)
	store.CleanupJobTemp(*job)
	store.RemoveJob(job.ID)
}

func finalizeSchedule(job *store.PendingJob, schedules *scheduler.Store) {
	if job.ScheduleID == "" || schedules == nil {
		return
	}
	if _, err := schedules.MarkDone(job.ScheduleID, job.ID, time.Now()); err != nil {
		if os.IsNotExist(err) {
			log.Printf("dropping orphaned schedule job %s for missing schedule %s", job.ID, job.ScheduleID)
		} else {
			log.Printf("schedule completion error for %s: %v", job.ScheduleID, err)
		}
	}
}

func runExecJob(job *store.PendingJob) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-l", "-c", job.Prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v: %s", err, stderr.String())
	}
	return stdout.String(), nil
}

func writeArtifact(job *store.PendingJob) {
	// Only persist artifacts for raw subagent runs, not synthesis.
	if job.Source != "subagent" {
		return
	}
	task := job.DelegatedTask
	if task == "" {
		task = job.Prompt
	}
	if len(task) > 200 {
		task = task[:200]
	}
	a := store.Artifact{
		ID:        store.NewArtifactID(),
		JobID:     job.ID,
		Backend:   job.State.Backend,
		Task:      task,
		ThreadID:  job.State.ThreadID,
		ParentJob: job.ParentJobID,
		Created:   time.Now(),
	}
	if err := store.WriteArtifact(a); err != nil {
		log.Printf("artifact write error for %s: %v", job.ID, err)
	}
}

func ProcessJob(job *store.PendingJob, client *oneagent.Client, schedules *scheduler.Store, callbacks Callbacks) {
	if job == nil {
		return
	}

	lock := jobLock(job)
	lock.Lock()
	defer lock.Unlock()
	processJob(job, client, schedules, callbacks)
}

func CanRetryJob(job store.PendingJob) bool {
	if job.TempPath == "" {
		return true
	}
	if _, err := os.Stat(job.TempPath); err == nil {
		return true
	} else if os.IsNotExist(err) {
		log.Printf("cannot retry job %s: missing temp file %s", job.ID, job.TempPath)
	} else {
		log.Printf("cannot retry job %s: temp file check failed for %s: %v", job.ID, job.TempPath, err)
	}
	return false
}

func RecoverPendingJobs(client *oneagent.Client, schedules *scheduler.Store, callbackFactory func(*store.PendingJob) Callbacks, filters ...func(store.PendingJob) bool) bool {
	storedJobs := store.ListJobs()
	if len(storedJobs) == 0 {
		return false
	}
	log.Printf("recovering %d pending job(s)", len(storedJobs))
	var filter func(store.PendingJob) bool
	if len(filters) > 0 {
		filter = filters[0]
	}
	recovered := false
	for _, storedJob := range storedJobs {
		if filter != nil && !filter(storedJob) {
			continue
		}
		recovered = true
		job := storedJob
		callbacks := Callbacks{}
		if callbackFactory != nil {
			callbacks = callbackFactory(&job)
		}
		switch job.Status {
		case "ready":
			log.Printf("replaying ready job %s", job.ID)
			ProcessJob(&job, client, schedules, callbacks)
		case "delivered":
			log.Printf("finalizing delivered job %s", job.ID)
			ProcessJob(&job, client, schedules, callbacks)
		case "running":
			if !CanRetryJob(job) {
				log.Printf("discarding interrupted job %s; source event may be retried", job.ID)
				if finalizeBlockingSubagentFailure(&job, "subagent was discarded during recovery because required temp files were missing") {
					continue
				}
				store.CleanupJobTemp(job)
				store.RemoveJob(job.ID)
				continue
			}
			log.Printf("retrying interrupted job %s", job.ID)
			ProcessJob(&job, client, schedules, callbacks)
		default:
			log.Printf("discarding unknown job state %q for %s", job.Status, job.ID)
			if finalizeBlockingSubagentFailure(&job, fmt.Sprintf("subagent was discarded during recovery because job state %q is unsupported", job.Status)) {
				continue
			}
			store.CleanupJobTemp(job)
			store.RemoveJob(job.ID)
		}
	}
	return recovered
}

func isRetryable(job store.PendingJob) bool {
	// "delivered" jobs are NOT retried here — they are finalized at startup
	// via RecoverPendingJobs. Including them caused infinite retry loops
	// when finalization was blocked or the process restarted before cleanup.
	return job.Status == "ready" || job.Status == "running"
}

func makeCallbacks(factory func(*store.PendingJob) Callbacks, job *store.PendingJob) Callbacks {
	if factory != nil {
		return factory(job)
	}
	return Callbacks{}
}

func retryLockedJob(storedJob store.PendingJob, client *oneagent.Client, schedules *scheduler.Store, callbackFactory func(*store.PendingJob) Callbacks, filter func(store.PendingJob) bool) bool {
	lock := jobLock(&storedJob)
	lock.Lock()
	defer lock.Unlock()

	job, ok := store.ReadJob(storedJob.ID)
	if !ok || !isRetryable(job) {
		return false
	}
	if filter != nil && !filter(job) {
		return false
	}

	log.Printf("retrying deliverable job %s (%s)", job.ID, job.Status)
	processJob(&job, client, schedules, makeCallbacks(callbackFactory, &job))
	return true
}

func RetryDeliverableJobs(client *oneagent.Client, schedules *scheduler.Store, callbackFactory func(*store.PendingJob) Callbacks, filters ...func(store.PendingJob) bool) bool {
	storedJobs := store.ListJobs()
	if len(storedJobs) == 0 {
		return false
	}

	var filter func(store.PendingJob) bool
	if len(filters) > 0 {
		filter = filters[0]
	}
	retried := false
	for _, storedJob := range storedJobs {
		if filter != nil && !filter(storedJob) {
			continue
		}
		if !isRetryable(storedJob) {
			continue
		}

		if retryLockedJob(storedJob, client, schedules, callbackFactory, filter) {
			retried = true
		}
	}
	return retried
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
	if client == nil || st.Backend == "" || st.ThreadID == "" {
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
