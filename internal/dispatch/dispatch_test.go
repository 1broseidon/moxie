package dispatch_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/scheduler"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

func useTempStoreDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	restore := store.SetConfigDir(dir)
	t.Cleanup(func() {
		dispatch.SetShuttingDown(false)
		restore()
	})
	return dir
}

func TestClearNativeSessionRemovesOnlyTargetBackend(t *testing.T) {
	storeDir := t.TempDir()
	client := &oneagent.Client{Store: oneagent.FilesystemStore{Dir: storeDir}}
	thread := &oneagent.Thread{
		ID: "thread-1",
		NativeSessions: map[string]string{
			"pi":     "stale-session",
			"claude": "live-session",
		},
	}
	if err := client.SaveThread(thread); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	if !dispatch.ClearNativeSession(client, store.State{Backend: "pi", ThreadID: thread.ID}) {
		t.Fatal("expected native session to be cleared")
	}

	got, err := client.LoadThread(thread.ID)
	if err != nil {
		t.Fatalf("load repaired thread: %v", err)
	}
	if _, ok := got.NativeSessions["pi"]; ok {
		t.Fatal("expected pi session to be removed")
	}
	if got.NativeSessions["claude"] != "live-session" {
		t.Fatalf("expected claude session to remain, got %q", got.NativeSessions["claude"])
	}
}

func TestClearNativeSessionNoopWithoutSavedSession(t *testing.T) {
	storeDir := t.TempDir()
	client := &oneagent.Client{Store: oneagent.FilesystemStore{Dir: storeDir}}
	thread := &oneagent.Thread{
		ID:             "thread-2",
		NativeSessions: map[string]string{"claude": "live-session"},
	}
	if err := client.SaveThread(thread); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	if dispatch.ClearNativeSession(client, store.State{Backend: "pi", ThreadID: thread.ID}) {
		t.Fatal("expected ClearNativeSession to report no change")
	}

	got, err := client.LoadThread(thread.ID)
	if err != nil {
		t.Fatalf("load thread: %v", err)
	}
	if got.NativeSessions["claude"] != "live-session" {
		t.Fatalf("expected claude session to remain, got %q", got.NativeSessions["claude"])
	}
}

func TestIsMissingNativeSessionError(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{name: "thread missing", msg: "Error: thread does not exist", want: true},
		{name: "session missing", msg: "session not found", want: true},
		{name: "conversation missing", msg: "No conversation found for id abc", want: true},
		{name: "api key", msg: "No API key found for google.", want: false},
		{name: "generic failure", msg: "exit status 1", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dispatch.IsMissingNativeSessionError(tt.msg); got != tt.want {
				t.Fatalf("dispatch.IsMissingNativeSessionError(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

func TestProcessJobRunsModelDeliversAndRemovesJob(t *testing.T) {
	useTempStoreDir(t)
	restoreRun := dispatch.SetRunModelFuncForTest(func(job *store.PendingJob, _ *oneagent.Client, onActivity func(string)) (string, bool) {
		if onActivity != nil {
			onActivity("Bash ls -la")
		}
		return "done", false
	})
	t.Cleanup(restoreRun)

	job := &store.PendingJob{ID: "job-101", ConversationID: "telegram:1"}
	var events []string
	dispatch.ProcessJob(job, nil, nil, dispatch.Callbacks{
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

	if store.JobExists(job.ID) {
		t.Fatalf("expected job %s to be removed", job.ID)
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
	restoreRun := dispatch.SetRunModelFuncForTest(func(job *store.PendingJob, _ *oneagent.Client, onActivity func(string)) (string, bool) {
		return "reply", false
	})
	t.Cleanup(restoreRun)

	job := &store.PendingJob{ID: "job-102", ConversationID: "telegram:1"}
	dispatch.ProcessJob(job, nil, nil, dispatch.Callbacks{
		OnResult: func(result string) error {
			if result != "reply" {
				t.Fatalf("OnResult result = %q, want reply", result)
			}
			return errors.New("telegram send failed")
		},
	})

	if !store.JobExists(job.ID) {
		t.Fatalf("expected job %s to remain persisted", job.ID)
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
	restoreRun := dispatch.SetRunModelFuncForTest(func(job *store.PendingJob, _ *oneagent.Client, onActivity func(string)) (string, bool) {
		return "", true
	})
	t.Cleanup(restoreRun)

	job := &store.PendingJob{ID: "job-103", ConversationID: "telegram:1"}
	resultCalled := false
	dispatch.ProcessJob(job, nil, nil, dispatch.Callbacks{
		OnResult: func(result string) error {
			resultCalled = true
			return nil
		},
	})

	if resultCalled {
		t.Fatal("expected OnResult not to be called for interrupted job")
	}
	if !store.JobExists(job.ID) {
		t.Fatalf("expected interrupted job %s to remain persisted", job.ID)
	}
	jobs := store.ListJobs()
	if len(jobs) != 1 || jobs[0].Status != "running" {
		t.Fatalf("persisted jobs = %+v, want one running job", jobs)
	}
}

func TestRecoverPendingJobsDiscardsRunningJobWithMissingTemp(t *testing.T) {
	useTempStoreDir(t)

	store.WriteJob(store.PendingJob{
		ID:             "job-104",
		Status:         "running",
		TempPath:       filepath.Join(t.TempDir(), "missing.txt"),
		ConversationID: "telegram:1",
		Prompt:         "hello",
		Updated:        time.Now(),
		State:          store.State{Backend: "claude", ThreadID: "chat-1"},
	})

	if !dispatch.RecoverPendingJobs(nil, nil, nil) {
		t.Fatal("expected RecoverPendingJobs to report work")
	}
	if store.JobExists("job-104") {
		t.Fatal("expected interrupted job with missing temp file to be removed")
	}
}

func TestRecoverPendingJobsReplaysReadyJobAndAdvancesCursor(t *testing.T) {
	useTempStoreDir(t)

	store.WriteJob(store.PendingJob{
		ID:             "job-105",
		SourceEventID:  "105",
		Source:         "telegram",
		Status:         "ready",
		ConversationID: "telegram:1",
		Result:         "ready result",
		State:          store.State{Backend: "claude", ThreadID: "chat-1"},
	})

	var seen string
	if !dispatch.RecoverPendingJobs(nil, nil, func(job *store.PendingJob) dispatch.Callbacks {
		return dispatch.Callbacks{
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
	if store.JobExists("job-105") {
		t.Fatal("expected replayed ready job to be removed")
	}
}

func TestRetryDeliverableJobsProcessesReadyAndDeliveredOnly(t *testing.T) {
	useTempStoreDir(t)

	store.WriteJob(store.PendingJob{ID: "job-201", Status: "ready", ConversationID: "telegram:1", Result: "ready"})
	store.WriteJob(store.PendingJob{ID: "job-202", Status: "delivered", ConversationID: "telegram:1", Result: "done"})
	store.WriteJob(store.PendingJob{ID: "job-203", Status: "running", ConversationID: "telegram:1", Prompt: "still running"})

	var resultIDs []string
	if !dispatch.RetryDeliverableJobs(nil, nil, func(job *store.PendingJob) dispatch.Callbacks {
		return dispatch.Callbacks{
			OnResult: func(result string) error {
				resultIDs = append(resultIDs, job.ID)
				return nil
			},
		}
	}) {
		t.Fatal("expected RetryDeliverableJobs to report work")
	}

	if !reflect.DeepEqual(resultIDs, []string{"job-201"}) {
		t.Fatalf("OnResult IDs = %v, want [job-201]", resultIDs)
	}
	if store.JobExists("job-201") || store.JobExists("job-202") {
		t.Fatal("expected ready and delivered jobs to be removed")
	}
	if !store.JobExists("job-203") {
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
	if _, err := schedules.AttachJob(sc.ID, "job-301"); err != nil {
		t.Fatalf("AttachJob(): %v", err)
	}

	job := &store.PendingJob{
		ID:             "job-301",
		ScheduleID:     sc.ID,
		Status:         "ready",
		ConversationID: "telegram:1",
		Result:         "sent",
	}

	dispatch.ProcessJob(job, nil, schedules, dispatch.Callbacks{
		OnResult: func(result string) error { return nil },
	})

	if _, err := schedules.Get(sc.ID); !os.IsNotExist(err) {
		t.Fatalf("expected one-shot schedule to be removed after delivery, err = %v", err)
	}
}
