package dispatch_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestClearNativeSessionUsesDefaultFilesystemStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	client := &oneagent.Client{}
	thread := &oneagent.Thread{
		ID:             "thread-default-store",
		NativeSessions: map[string]string{"pi": "stale-session"},
	}
	if err := (oneagent.FilesystemStore{}).SaveThread(thread); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	if !dispatch.ClearNativeSession(client, store.State{Backend: "pi", ThreadID: thread.ID}) {
		t.Fatal("expected native session to be cleared from default store")
	}

	got, err := (oneagent.FilesystemStore{}).LoadThread(thread.ID)
	if err != nil {
		t.Fatalf("load thread: %v", err)
	}
	if _, ok := got.NativeSessions["pi"]; ok {
		t.Fatal("expected pi session to be removed")
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

func TestProcessJobInterruptedBlockingSubagentWritesFailureSentinel(t *testing.T) {
	useTempStoreDir(t)
	store.SaveConfig(store.Config{
		Channels: map[string]store.ChannelConfig{
			"telegram": {
				Provider:  "telegram",
				Token:     "token",
				ChannelID: "123",
			},
		},
		SubagentMaxAttempts:     1,
		SubagentStallTimeout:    "1s",
		SubagentProgressTimeout: "1s",
		SubagentRetryBackoff:    []string{"0s"},
	})

	dispatch.SetShuttingDown(true)
	t.Cleanup(func() { dispatch.SetShuttingDown(false) })

	restoreRun := dispatch.SetRunStreamModelFuncForTest(func(ctx context.Context, job *store.PendingJob, _ *oneagent.Client, emit func(oneagent.StreamEvent)) oneagent.Response {
		return oneagent.Response{Error: context.Canceled.Error(), Backend: job.State.Backend, ThreadID: job.State.ThreadID}
	})
	t.Cleanup(restoreRun)

	resultPath := filepath.Join(store.JobsDir(), "job-103-blocking.result")
	job := &store.PendingJob{
		ID:                 "job-103-blocking",
		ConversationID:     "telegram:1",
		Source:             "subagent",
		DelegatedTask:      "inspect the blocked child",
		BlockingResultPath: resultPath,
		State:              store.State{Backend: "codex", ThreadID: "sub-thread"},
	}

	resultCalled := false
	dispatch.ProcessJob(job, &oneagent.Client{}, nil, dispatch.Callbacks{
		OnResult: func(string) error {
			resultCalled = true
			return nil
		},
	})

	if resultCalled {
		t.Fatal("expected OnResult not to be called for interrupted blocking subagent")
	}
	if store.JobExists(job.ID) {
		t.Fatalf("expected interrupted blocking job %s to be removed", job.ID)
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", resultPath, err)
	}
	for _, want := range []string{
		"Blocking subagent failed before producing a normal result.",
		"Backend: codex",
		"Task: inspect the blocked child",
		"Reason: subagent interrupted during shutdown before producing a normal result",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("blocking failure result = %q, want substring %q", string(data), want)
		}
	}
}

func TestProcessJobAllowsParallelDifferentConversations(t *testing.T) {
	useTempStoreDir(t)

	started := make(chan string, 2)
	release := make(chan struct{})
	restoreRun := dispatch.SetRunModelFuncForTest(func(job *store.PendingJob, _ *oneagent.Client, _ func(string)) (string, bool) {
		started <- job.ConversationID
		<-release
		return "done", false
	})
	t.Cleanup(restoreRun)

	done := make(chan struct{}, 2)
	runJob := func(id, conversationID string) {
		dispatch.ProcessJob(&store.PendingJob{
			ID:             id,
			ConversationID: conversationID,
		}, nil, nil, dispatch.Callbacks{
			OnResult: func(result string) error { return nil },
			OnDone: func() {
				done <- struct{}{}
			},
		})
	}

	go runJob("job-a", "telegram:1")
	go runJob("job-b", "slack:C1")

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case conversationID := <-started:
			seen[conversationID] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("expected both jobs to enter runModelFunc, saw %v", seen)
		}
	}

	close(release)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for parallel jobs to finish")
		}
	}
}

func TestProcessJobSerializesJobsOnSameThreadAcrossSources(t *testing.T) {
	useTempStoreDir(t)

	started := make(chan string, 2)
	releaseFirst := make(chan struct{})
	restoreRun := dispatch.SetRunModelFuncForTest(func(job *store.PendingJob, _ *oneagent.Client, _ func(string)) (string, bool) {
		started <- job.ID
		if job.ID == "job-thread-1" {
			<-releaseFirst
		}
		return "done", false
	})
	t.Cleanup(restoreRun)

	done := make(chan string, 2)
	runJob := func(job *store.PendingJob) {
		dispatch.ProcessJob(job, nil, nil, dispatch.Callbacks{
			OnResult: func(string) error { return nil },
			OnDone: func() {
				done <- job.ID
			},
		})
	}

	go runJob(&store.PendingJob{
		ID:             "job-thread-1",
		ConversationID: "telegram:1",
		Source:         "subagent",
		State:          store.State{Backend: "claude", ThreadID: "shared-thread"},
	})

	select {
	case got := <-started:
		if got != "job-thread-1" {
			t.Fatalf("first started job = %q, want job-thread-1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first job to start")
	}

	go runJob(&store.PendingJob{
		ID:             "job-thread-2",
		ConversationID: "slack:C123",
		Source:         "subagent-synthesis",
		State:          store.State{Backend: "claude", ThreadID: "shared-thread"},
	})

	select {
	case got := <-started:
		t.Fatalf("second job %q started before shared thread lock released", got)
	case <-time.After(150 * time.Millisecond):
	}

	close(releaseFirst)

	select {
	case got := <-started:
		if got != "job-thread-2" {
			t.Fatalf("second started job = %q, want job-thread-2", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second job to start")
	}

	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for serialized jobs to finish")
		}
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

func TestRecoverPendingJobsDiscardsBlockingSubagentWithMissingTempWritesFailureSentinel(t *testing.T) {
	useTempStoreDir(t)

	resultPath := filepath.Join(store.JobsDir(), "job-104-blocking.result")
	store.WriteJob(store.PendingJob{
		ID:                 "job-104-blocking",
		Status:             "running",
		Source:             "subagent",
		TempPath:           filepath.Join(t.TempDir(), "missing.txt"),
		ConversationID:     "telegram:1",
		DelegatedTask:      "recover the nested child",
		BlockingResultPath: resultPath,
		Updated:            time.Now(),
		State:              store.State{Backend: "claude", ThreadID: "chat-1"},
	})

	if !dispatch.RecoverPendingJobs(nil, nil, nil) {
		t.Fatal("expected RecoverPendingJobs to report work")
	}
	if store.JobExists("job-104-blocking") {
		t.Fatal("expected blocking job with missing temp file to be removed")
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", resultPath, err)
	}
	for _, want := range []string{
		"Blocking subagent failed before producing a normal result.",
		"Backend: claude",
		"Task: recover the nested child",
		"Reason: subagent was discarded during recovery because required temp files were missing",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("blocking recovery failure = %q, want substring %q", string(data), want)
		}
	}
}

func TestRecoverPendingJobsDiscardsBlockingSubagentWithUnknownStateWritesFailureSentinel(t *testing.T) {
	useTempStoreDir(t)

	resultPath := filepath.Join(store.JobsDir(), "job-104-unknown.result")
	store.WriteJob(store.PendingJob{
		ID:                 "job-104-unknown",
		Status:             "mystery",
		Source:             "subagent",
		ConversationID:     "telegram:1",
		DelegatedTask:      "recover the unknown nested child",
		BlockingResultPath: resultPath,
		Updated:            time.Now(),
		State:              store.State{Backend: "pi", ThreadID: "chat-1"},
	})

	if !dispatch.RecoverPendingJobs(nil, nil, nil) {
		t.Fatal("expected RecoverPendingJobs to report work")
	}
	if store.JobExists("job-104-unknown") {
		t.Fatal("expected blocking job with unknown state to be removed")
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", resultPath, err)
	}
	for _, want := range []string{
		"Blocking subagent failed before producing a normal result.",
		"Backend: pi",
		"Task: recover the unknown nested child",
		"Reason: subagent was discarded during recovery because job state \"mystery\" is unsupported",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("blocking recovery failure = %q, want substring %q", string(data), want)
		}
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

func TestDiscardPendingJobsRemovesOnlyMatchingJobs(t *testing.T) {
	useTempStoreDir(t)

	store.WriteJob(store.PendingJob{ID: "job-tg", Status: "ready", Source: "telegram", ConversationID: "telegram:1"})
	store.WriteJob(store.PendingJob{ID: "job-slack", Status: "running", Source: "slack", ConversationID: "slack:C1"})

	discarded := dispatch.DiscardPendingJobs("discarded on startup", func(job store.PendingJob) bool {
		return job.Source == "telegram"
	})
	if !discarded {
		t.Fatal("expected DiscardPendingJobs to report discarded work")
	}
	if store.JobExists("job-tg") {
		t.Fatal("expected matching job to be removed")
	}
	if !store.JobExists("job-slack") {
		t.Fatal("expected non-matching job to remain")
	}
}

func TestDiscardPendingJobsWritesBlockingFailureSentinel(t *testing.T) {
	useTempStoreDir(t)

	resultPath := filepath.Join(store.JobsDir(), "job-blocking.result")
	store.WriteJob(store.PendingJob{
		ID:                 "job-blocking",
		Status:             "running",
		Source:             "subagent",
		ConversationID:     "telegram:1",
		DelegatedTask:      "discard on startup",
		BlockingResultPath: resultPath,
		Updated:            time.Now(),
		State:              store.State{Backend: "claude", ThreadID: "chat-1"},
	})

	if !dispatch.DiscardPendingJobs("subagent was discarded on startup because recover_pending_jobs_on_startup=false") {
		t.Fatal("expected DiscardPendingJobs to report discarded work")
	}
	if store.JobExists("job-blocking") {
		t.Fatal("expected blocking job to be removed")
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", resultPath, err)
	}
	for _, want := range []string{
		"Blocking subagent failed before producing a normal result.",
		"Backend: claude",
		"Task: discard on startup",
		"Reason: subagent was discarded on startup because recover_pending_jobs_on_startup=false",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("blocking discard failure = %q, want substring %q", string(data), want)
		}
	}
}

func TestRetryDeliverableJobsProcessesReadyAndRunningButNotDelivered(t *testing.T) {
	useTempStoreDir(t)

	store.WriteJob(store.PendingJob{ID: "job-201", Status: "ready", ConversationID: "telegram:1", Result: "ready"})
	store.WriteJob(store.PendingJob{ID: "job-202", Status: "delivered", ConversationID: "telegram:1", Result: "done"})
	store.WriteJob(store.PendingJob{ID: "job-203", Status: "running", ConversationID: "telegram:1", Prompt: "still running"})

	restoreRun := dispatch.SetRunModelFuncForTest(func(job *store.PendingJob, _ *oneagent.Client, _ func(string)) (string, bool) {
		return "finished running", false
	})
	defer restoreRun()

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

	var found201, found203 bool
	for _, id := range resultIDs {
		if id == "job-201" {
			found201 = true
		}
		if id == "job-203" {
			found203 = true
		}
	}
	if len(resultIDs) != 2 || !found201 || !found203 {
		t.Fatalf("OnResult IDs = %v, want [job-201, job-203] (any order)", resultIDs)
	}
	if store.JobExists("job-201") || store.JobExists("job-203") {
		t.Fatal("expected retried jobs to be removed after successful retry")
	}
	// "delivered" jobs are NOT retried — they are finalized at startup via
	// RecoverPendingJobs. The retry loop must leave them untouched to avoid
	// infinite retry loops when finalization is blocked.
	if !store.JobExists("job-202") {
		t.Fatal("expected delivered job to be left for startup recovery, not retried")
	}
}

func TestRetryDeliverableJobsSkipsStaleReadySnapshotAfterLiveDelivery(t *testing.T) {
	useTempStoreDir(t)

	restoreRun := dispatch.SetRunModelFuncForTest(func(job *store.PendingJob, _ *oneagent.Client, _ func(string)) (string, bool) {
		return "done", false
	})
	t.Cleanup(restoreRun)

	readyPersisted := make(chan struct{})
	releaseOriginal := make(chan struct{})
	originalDone := make(chan struct{})
	deliveries := make(chan string, 2)

	job := &store.PendingJob{ID: "job-stale", ConversationID: "telegram:1"}
	go dispatch.ProcessJob(job, nil, nil, dispatch.Callbacks{
		OnStatusClear: func() {
			select {
			case <-readyPersisted:
			default:
				close(readyPersisted)
			}
		},
		OnResult: func(result string) error {
			deliveries <- "original"
			<-releaseOriginal
			return nil
		},
		OnDone: func() {
			close(originalDone)
		},
	})

	select {
	case <-readyPersisted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for live job to persist ready state")
	}

	retryDone := make(chan bool, 1)
	go func() {
		retryDone <- dispatch.RetryDeliverableJobs(nil, nil, func(job *store.PendingJob) dispatch.Callbacks {
			return dispatch.Callbacks{
				OnResult: func(result string) error {
					deliveries <- "retry"
					return nil
				},
			}
		})
	}()
	time.Sleep(100 * time.Millisecond)

	close(releaseOriginal)

	select {
	case <-originalDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for original job to complete")
	}

	select {
	case retried := <-retryDone:
		if retried {
			t.Fatal("expected retry loop to skip stale ready snapshot after live delivery")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for retry loop to finish")
	}

	close(deliveries)
	var got []string
	for delivery := range deliveries {
		got = append(got, delivery)
	}
	if !reflect.DeepEqual(got, []string{"original"}) {
		t.Fatalf("deliveries = %v, want [original]", got)
	}
	if store.JobExists(job.ID) {
		t.Fatalf("expected job %s to be removed after original delivery", job.ID)
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

func TestProcessJobDropsDeliveredScheduleJobWhenScheduleMissing(t *testing.T) {
	dir := useTempStoreDir(t)
	schedules := scheduler.NewStore(filepath.Join(dir, "schedules.json"), time.FixedZone("CDT", -5*60*60))

	job := &store.PendingJob{
		ID:             "job-302",
		ScheduleID:     "sch-missing",
		Status:         "delivered",
		ConversationID: "telegram:1",
		Result:         "sent",
	}
	store.WriteJob(*job)

	dispatch.ProcessJob(job, nil, schedules, dispatch.Callbacks{})

	if store.JobExists(job.ID) {
		t.Fatalf("expected orphaned delivered schedule job %s to be removed", job.ID)
	}
}
