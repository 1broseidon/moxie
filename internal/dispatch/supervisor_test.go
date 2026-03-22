package dispatch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

func writeSupervisionConfig(t *testing.T, attempts int, stallTimeout, progressTimeout string, backoff ...string) {
	t.Helper()
	store.SaveConfig(store.Config{
		Channels: map[string]store.ChannelConfig{
			"telegram": {
				Provider:  "telegram",
				Token:     "token",
				ChannelID: "123",
			},
		},
		SubagentMaxAttempts:     attempts,
		SubagentStallTimeout:    stallTimeout,
		SubagentProgressTimeout: progressTimeout,
		SubagentRetryBackoff:    backoff,
	})
}

func TestProcessJobSupervisedSubagentSuccess(t *testing.T) {
	useTempStoreDir(t)
	writeSupervisionConfig(t, 3, "1s", "1s", "0s", "0s", "0s")

	restorePoll := dispatch.SetSupervisionPollIntervalForTest(5 * time.Millisecond)
	t.Cleanup(restorePoll)

	base := time.Date(2026, 3, 22, 18, 0, 0, 0, time.UTC)
	release := make(chan struct{})
	restoreRun := dispatch.SetRunStreamModelFuncForTest(func(ctx context.Context, job *store.PendingJob, _ *oneagent.Client, emit func(oneagent.StreamEvent)) oneagent.Response {
		emit(oneagent.StreamEvent{Type: "start", RunID: "run-1", TS: base})
		emit(oneagent.StreamEvent{Type: "session", RunID: "run-1", TS: base.Add(time.Millisecond), Session: "sess-1"})
		emit(oneagent.StreamEvent{Type: "activity", RunID: "run-1", TS: base.Add(2 * time.Millisecond), Activity: "inspect repo"})
		<-release
		return oneagent.Response{Result: "ok", Backend: job.State.Backend}
	})
	t.Cleanup(restoreRun)

	job := &store.PendingJob{
		ID:             "job-supervised-success",
		ConversationID: "telegram:1",
		Source:         "subagent",
		Prompt:         "inspect the repo",
		State:          store.State{Backend: "claude", ThreadID: "sub-thread"},
	}

	activitySeen := make(chan string, 1)
	done := make(chan struct{})
	var gotResult string
	go dispatch.ProcessJob(job, &oneagent.Client{}, nil, dispatch.Callbacks{
		OnActivity: func(activity string) {
			activitySeen <- activity
		},
		OnResult: func(result string) error {
			gotResult = result
			return nil
		},
		OnDone: func() {
			close(done)
		},
	})

	select {
	case activity := <-activitySeen:
		if activity != "inspect repo" {
			t.Fatalf("OnActivity = %q, want inspect repo", activity)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for supervised activity")
	}

	stored, ok := store.ReadJob(job.ID)
	if !ok {
		t.Fatalf("expected persisted running job %s", job.ID)
	}
	if stored.Supervision.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1", stored.Supervision.Attempt)
	}
	if stored.Supervision.MaxAttempts != 3 {
		t.Fatalf("max_attempts = %d, want 3", stored.Supervision.MaxAttempts)
	}
	if stored.Supervision.ActiveRunID != "run-1" {
		t.Fatalf("active_run_id = %q, want run-1", stored.Supervision.ActiveRunID)
	}
	wantTS := base.Add(2 * time.Millisecond)
	if !stored.Supervision.LastEventAt.Equal(wantTS) {
		t.Fatalf("last_event_at = %v, want %v", stored.Supervision.LastEventAt, wantTS)
	}
	if !stored.Supervision.LastProgressAt.Equal(wantTS) {
		t.Fatalf("last_progress_at = %v, want %v", stored.Supervision.LastProgressAt, wantTS)
	}
	if stored.Supervision.LastError != "" {
		t.Fatalf("last_error = %q, want empty", stored.Supervision.LastError)
	}

	close(release)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for supervised job to finish")
	}

	if gotResult != "ok" {
		t.Fatalf("result = %q, want ok", gotResult)
	}
	if store.JobExists(job.ID) {
		t.Fatalf("expected job %s to be removed after delivery", job.ID)
	}
}

func TestProcessJobSupervisedSubagentRetriesOnProgressStall(t *testing.T) {
	useTempStoreDir(t)
	writeSupervisionConfig(t, 2, "250ms", "30ms", "0s", "0s")

	restorePoll := dispatch.SetSupervisionPollIntervalForTest(5 * time.Millisecond)
	t.Cleanup(restorePoll)

	var attempts int
	var threadIDs []string
	releaseSecond := make(chan struct{})
	restoreRun := dispatch.SetRunStreamModelFuncForTest(func(ctx context.Context, job *store.PendingJob, _ *oneagent.Client, emit func(oneagent.StreamEvent)) oneagent.Response {
		attempts++
		threadIDs = append(threadIDs, job.State.ThreadID)

		switch attempts {
		case 1:
			emit(oneagent.StreamEvent{Type: "start", RunID: "run-1", TS: time.Now().UTC()})
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return oneagent.Response{Error: ctx.Err().Error(), Backend: job.State.Backend}
				case ts := <-ticker.C:
					emit(oneagent.StreamEvent{Type: "heartbeat", RunID: "run-1", TS: ts.UTC()})
				}
			}
		case 2:
			base := time.Date(2026, 3, 22, 18, 10, 0, 0, time.UTC)
			emit(oneagent.StreamEvent{Type: "start", RunID: "run-2", TS: base})
			emit(oneagent.StreamEvent{Type: "activity", RunID: "run-2", TS: base.Add(time.Millisecond), Activity: "retry succeeded"})
			<-releaseSecond
			return oneagent.Response{Result: "recovered", Backend: job.State.Backend}
		default:
			return oneagent.Response{Error: "unexpected attempt", Backend: job.State.Backend}
		}
	})
	t.Cleanup(restoreRun)

	job := &store.PendingJob{
		ID:             "job-supervised-retry",
		ConversationID: "telegram:1",
		Source:         "subagent",
		Prompt:         "retry when stalled",
		State:          store.State{Backend: "claude", ThreadID: "sub-thread"},
	}

	activitySeen := make(chan string, 1)
	done := make(chan struct{})
	var gotResult string
	go dispatch.ProcessJob(job, &oneagent.Client{}, nil, dispatch.Callbacks{
		OnActivity: func(activity string) {
			activitySeen <- activity
		},
		OnResult: func(result string) error {
			gotResult = result
			return nil
		},
		OnDone: func() {
			close(done)
		},
	})

	select {
	case activity := <-activitySeen:
		if activity != "retry succeeded" {
			t.Fatalf("OnActivity = %q, want retry succeeded", activity)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for retried attempt")
	}

	stored, ok := store.ReadJob(job.ID)
	if !ok {
		t.Fatalf("expected persisted running job %s during retry", job.ID)
	}
	if stored.Supervision.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", stored.Supervision.Attempt)
	}
	if stored.Supervision.ActiveRunID != "run-2" {
		t.Fatalf("active_run_id = %q, want run-2", stored.Supervision.ActiveRunID)
	}
	if stored.State.ThreadID == "sub-thread" {
		t.Fatalf("expected retry to use a fresh thread id, got %q", stored.State.ThreadID)
	}

	close(releaseSecond)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for retried job to finish")
	}

	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(threadIDs) != 2 {
		t.Fatalf("thread ids = %v, want 2 entries", threadIDs)
	}
	if threadIDs[0] != "sub-thread" {
		t.Fatalf("first thread id = %q, want sub-thread", threadIDs[0])
	}
	if threadIDs[1] == "" || threadIDs[1] == threadIDs[0] {
		t.Fatalf("second thread id = %q, want a fresh thread id", threadIDs[1])
	}
	if gotResult != "recovered" {
		t.Fatalf("result = %q, want recovered", gotResult)
	}
}

func TestProcessJobSupervisedSubagentReturnsTerminalFailureAfterMaxAttempts(t *testing.T) {
	useTempStoreDir(t)
	writeSupervisionConfig(t, 2, "250ms", "30ms", "0s", "0s")

	restorePoll := dispatch.SetSupervisionPollIntervalForTest(5 * time.Millisecond)
	t.Cleanup(restorePoll)

	var attempts int
	restoreRun := dispatch.SetRunStreamModelFuncForTest(func(ctx context.Context, job *store.PendingJob, _ *oneagent.Client, emit func(oneagent.StreamEvent)) oneagent.Response {
		attempts++
		emit(oneagent.StreamEvent{Type: "start", RunID: "run-terminal", TS: time.Now().UTC()})
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return oneagent.Response{Error: ctx.Err().Error(), Backend: job.State.Backend}
			case ts := <-ticker.C:
				emit(oneagent.StreamEvent{Type: "heartbeat", RunID: "run-terminal", TS: ts.UTC()})
			}
		}
	})
	t.Cleanup(restoreRun)

	job := &store.PendingJob{
		ID:             "job-supervised-terminal-failure",
		ConversationID: "telegram:1",
		Source:         "subagent",
		Prompt:         "give up after max attempts",
		State:          store.State{Backend: "claude", ThreadID: "sub-thread"},
	}

	var gotResult string
	dispatch.ProcessJob(job, &oneagent.Client{}, nil, dispatch.Callbacks{
		OnResult: func(result string) error {
			gotResult = result
			return nil
		},
	})

	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if !strings.Contains(gotResult, "no progress") {
		t.Fatalf("result = %q, want progress stall error", gotResult)
	}
	if store.JobExists(job.ID) {
		t.Fatalf("expected job %s to be removed after terminal failure delivery", job.ID)
	}
}

func TestProcessJobSupervisedSubagentIgnoresStaleRunIDEvents(t *testing.T) {
	useTempStoreDir(t)
	writeSupervisionConfig(t, 1, "1s", "1s", "0s")

	restorePoll := dispatch.SetSupervisionPollIntervalForTest(5 * time.Millisecond)
	t.Cleanup(restorePoll)

	goodTS := time.Date(2026, 3, 22, 18, 20, 0, 0, time.UTC)
	staleTS := goodTS.Add(time.Second)
	release := make(chan struct{})
	staleSent := make(chan struct{})
	restoreRun := dispatch.SetRunStreamModelFuncForTest(func(ctx context.Context, job *store.PendingJob, _ *oneagent.Client, emit func(oneagent.StreamEvent)) oneagent.Response {
		emit(oneagent.StreamEvent{Type: "start", RunID: "run-good", TS: goodTS.Add(-time.Millisecond)})
		emit(oneagent.StreamEvent{Type: "activity", RunID: "run-good", TS: goodTS, Activity: "real activity"})
		emit(oneagent.StreamEvent{Type: "activity", RunID: "run-stale", TS: staleTS, Activity: "ignore me"})
		close(staleSent)
		<-release
		return oneagent.Response{Result: "ok", Backend: job.State.Backend}
	})
	t.Cleanup(restoreRun)

	job := &store.PendingJob{
		ID:             "job-supervised-stale-run",
		ConversationID: "telegram:1",
		Source:         "subagent",
		Prompt:         "ignore stale events",
		State:          store.State{Backend: "claude", ThreadID: "sub-thread"},
	}

	activitySeen := make(chan string, 2)
	done := make(chan struct{})
	go dispatch.ProcessJob(job, &oneagent.Client{}, nil, dispatch.Callbacks{
		OnActivity: func(activity string) {
			activitySeen <- activity
		},
		OnResult: func(result string) error { return nil },
		OnDone: func() {
			close(done)
		},
	})

	select {
	case activity := <-activitySeen:
		if activity != "real activity" {
			t.Fatalf("first activity = %q, want real activity", activity)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for real activity")
	}

	select {
	case <-staleSent:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stale event")
	}
	time.Sleep(20 * time.Millisecond)

	select {
	case activity := <-activitySeen:
		t.Fatalf("unexpected stale activity callback: %q", activity)
	default:
	}

	stored, ok := store.ReadJob(job.ID)
	if !ok {
		t.Fatalf("expected persisted running job %s", job.ID)
	}
	if stored.Supervision.ActiveRunID != "run-good" {
		t.Fatalf("active_run_id = %q, want run-good", stored.Supervision.ActiveRunID)
	}
	if !stored.Supervision.LastEventAt.Equal(goodTS) {
		t.Fatalf("last_event_at = %v, want %v", stored.Supervision.LastEventAt, goodTS)
	}
	if !stored.Supervision.LastProgressAt.Equal(goodTS) {
		t.Fatalf("last_progress_at = %v, want %v", stored.Supervision.LastProgressAt, goodTS)
	}

	close(release)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stale-run test job to finish")
	}
}
