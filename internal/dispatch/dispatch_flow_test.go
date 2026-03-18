package dispatch

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/1broseidon/moxie/internal/scheduler"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

func useTempStoreDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	restore := store.SetConfigDir(dir)
	prevRunner := runModelFunc
	t.Cleanup(func() {
		runModelFunc = prevRunner
		SetShuttingDown(false)
		restore()
	})
	return dir
}

func TestProcessJobRunsModelDeliversAndRemovesJob(t *testing.T) {
	useTempStoreDir(t)

	runModelFunc = func(job *store.PendingJob, _ *oneagent.Client, onActivity func(string)) (string, bool) {
		if onActivity != nil {
			onActivity("Bash ls -la")
		}
		return "done", false
	}

	job := &store.PendingJob{UpdateID: 101, ChatID: 1}
	var events []string
	ProcessJob(job, nil, nil, Callbacks{
		OnActivity: func(activity string) {
			events = append(events, "activity:"+activity)
		},
		OnStatusClear: func() {
			events = append(events, "clear")
		},
		OnResult: func(result string) error {
			events = append(events, "result:"+result)
			return nil
		},
		OnDone: func() {
			events = append(events, "done")
		},
	})

	if store.JobExists(job.UpdateID) {
		t.Fatalf("expected job %d to be removed", job.UpdateID)
	}
	if job.Status != "delivered" {
		t.Fatalf("job status = %q, want delivered", job.Status)
	}
	if job.Result != "done" {
		t.Fatalf("job result = %q, want done", job.Result)
	}

	want := []string{"activity:Bash ls -la", "clear", "result:done", "done"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestProcessJobDeliveryErrorLeavesReadyJob(t *testing.T) {
	useTempStoreDir(t)

	runModelFunc = func(job *store.PendingJob, _ *oneagent.Client, onActivity func(string)) (string, bool) {
		return "reply", false
	}

	job := &store.PendingJob{UpdateID: 102, ChatID: 1}
	ProcessJob(job, nil, nil, Callbacks{
		OnResult: func(result string) error {
			if result != "reply" {
				t.Fatalf("OnResult result = %q, want reply", result)
			}
			return errors.New("telegram send failed")
		},
	})

	if !store.JobExists(job.UpdateID) {
		t.Fatalf("expected job %d to remain persisted", job.UpdateID)
	}
	jobs := store.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("ListJobs() len = %d, want 1", len(jobs))
	}
	if jobs[0].Status != "ready" {
		t.Fatalf("persisted job status = %q, want ready", jobs[0].Status)
	}
	if jobs[0].Result != "reply" {
		t.Fatalf("persisted job result = %q, want reply", jobs[0].Result)
	}
}

func TestProcessJobInterruptedLeavesRunningJob(t *testing.T) {
	useTempStoreDir(t)

	runModelFunc = func(job *store.PendingJob, _ *oneagent.Client, onActivity func(string)) (string, bool) {
		return "", true
	}

	job := &store.PendingJob{UpdateID: 103, ChatID: 1}
	resultCalled := false
	ProcessJob(job, nil, nil, Callbacks{
		OnResult: func(result string) error {
			resultCalled = true
			return nil
		},
	})

	if resultCalled {
		t.Fatal("expected OnResult not to be called for interrupted job")
	}
	if !store.JobExists(job.UpdateID) {
		t.Fatalf("expected interrupted job %d to remain persisted", job.UpdateID)
	}
	jobs := store.ListJobs()
	if len(jobs) != 1 || jobs[0].Status != "running" {
		t.Fatalf("persisted jobs = %+v, want one running job", jobs)
	}
}

func TestRecoverPendingJobsDiscardsRunningJobWithMissingTemp(t *testing.T) {
	useTempStoreDir(t)

	store.WriteJob(store.PendingJob{
		UpdateID:   104,
		Status:     "running",
		TempPath:   filepath.Join(t.TempDir(), "missing.txt"),
		ChatID:     1,
		Prompt:     "hello",
		Updated:    time.Now(),
		State:      store.State{Backend: "claude", ThreadID: "tg-1"},
		Result:     "",
		ScheduleID: "",
	})

	if !RecoverPendingJobs(nil, nil, nil) {
		t.Fatal("expected RecoverPendingJobs to report work")
	}
	if store.JobExists(104) {
		t.Fatal("expected interrupted job with missing temp file to be removed")
	}
}

func TestRecoverPendingJobsReplaysReadyJobAndAdvancesCursor(t *testing.T) {
	useTempStoreDir(t)

	store.WriteJob(store.PendingJob{
		UpdateID: 105,
		Status:   "ready",
		ChatID:   1,
		Result:   "ready result",
		State:    store.State{Backend: "claude", ThreadID: "tg-1"},
	})

	var seen string
	if !RecoverPendingJobs(nil, nil, func(job *store.PendingJob) Callbacks {
		return Callbacks{
			OnResult: func(result string) error {
				seen = result
				return nil
			},
		}
	}) {
		t.Fatal("expected RecoverPendingJobs to report work")
	}

	if seen != "ready result" {
		t.Fatalf("OnResult saw %q, want ready result", seen)
	}
	if store.JobExists(105) {
		t.Fatal("expected replayed ready job to be removed")
	}
	if got := store.ReadCursor(); got != 105 {
		t.Fatalf("cursor = %d, want 105", got)
	}
}

func TestRetryDeliverableJobsProcessesReadyAndDeliveredOnly(t *testing.T) {
	useTempStoreDir(t)

	store.WriteJob(store.PendingJob{UpdateID: 201, Status: "ready", ChatID: 1, Result: "ready"})
	store.WriteJob(store.PendingJob{UpdateID: 202, Status: "delivered", ChatID: 1, Result: "done"})
	store.WriteJob(store.PendingJob{UpdateID: 203, Status: "running", ChatID: 1, Prompt: "still running"})

	var resultIDs []int
	if !RetryDeliverableJobs(nil, nil, func(job *store.PendingJob) Callbacks {
		return Callbacks{
			OnResult: func(result string) error {
				resultIDs = append(resultIDs, job.UpdateID)
				return nil
			},
		}
	}) {
		t.Fatal("expected RetryDeliverableJobs to report work")
	}

	if !reflect.DeepEqual(resultIDs, []int{201}) {
		t.Fatalf("OnResult IDs = %v, want [201]", resultIDs)
	}
	if store.JobExists(201) || store.JobExists(202) {
		t.Fatal("expected ready and delivered jobs to be removed")
	}
	if !store.JobExists(203) {
		t.Fatal("expected running job to remain")
	}
}

func TestProcessJobCompletesOneShotScheduleAfterDelivery(t *testing.T) {
	dir := useTempStoreDir(t)
	schedules := scheduler.NewStore(filepath.Join(dir, "schedules.json"), time.FixedZone("CDT", -5*60*60))
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := schedules.Add(scheduler.AddInput{
		Trigger: scheduler.TriggerAt,
		Action:  scheduler.ActionSend,
		At:      "2026-03-18T10:00:00-05:00",
		Text:    "remind me",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}
	if _, err := schedules.AttachJob(sc.ID, 301); err != nil {
		t.Fatalf("AttachJob(): %v", err)
	}

	job := &store.PendingJob{
		UpdateID:   301,
		ScheduleID: sc.ID,
		Status:     "ready",
		ChatID:     1,
		Result:     "sent",
	}

	ProcessJob(job, nil, schedules, Callbacks{
		OnResult: func(result string) error { return nil },
	})

	if _, err := schedules.Get(sc.ID); !os.IsNotExist(err) {
		t.Fatalf("expected one-shot schedule to be removed after delivery, err = %v", err)
	}
}
