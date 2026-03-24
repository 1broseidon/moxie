package dispatch_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/oneagent"
	"github.com/1broseidon/moxie/internal/store"
)

// ---------------------------------------------------------------------------
// Bug class: delivered jobs retried indefinitely in hot loop
// Root cause: isRetryable included "delivered" status
// ---------------------------------------------------------------------------

// TestRetryLoopDoesNotRetryDeliveredJobs verifies that the periodic retry
// loop ignores jobs with status "delivered". These jobs should only be
// cleaned up at startup via RecoverPendingJobs.
func TestRetryLoopDoesNotRetryDeliveredJobs(t *testing.T) {
	useTempStoreDir(t)

	store.WriteJob(store.PendingJob{
		ID:             "job-delivered-1",
		Status:         "delivered",
		ConversationID: "telegram:1",
		Result:         "already sent",
	})

	resultCalled := false
	retried := dispatch.RetryDeliverableJobs(nil, nil, func(job *store.PendingJob) dispatch.Callbacks {
		return dispatch.Callbacks{
			OnResult: func(result string) error {
				resultCalled = true
				return nil
			},
		}
	})

	if retried {
		t.Fatal("retry loop should not process delivered jobs")
	}
	if resultCalled {
		t.Fatal("OnResult must not be called for delivered jobs")
	}
	if !store.JobExists("job-delivered-1") {
		t.Fatal("delivered job must remain on disk for startup recovery")
	}
}

// TestRetryLoopRepeatedInvocationsNeverTouchDeliveredJob simulates the
// ticker firing N times and confirms that a "delivered" job is never
// processed — the exact scenario that caused the 2.5-hour zombie loop.
func TestRetryLoopRepeatedInvocationsNeverTouchDeliveredJob(t *testing.T) {
	useTempStoreDir(t)

	store.WriteJob(store.PendingJob{
		ID:             "job-zombie",
		Status:         "delivered",
		ConversationID: "telegram:1",
		Result:         "zombie payload",
	})

	var processCount int
	for i := 0; i < 50; i++ {
		dispatch.RetryDeliverableJobs(nil, nil, func(job *store.PendingJob) dispatch.Callbacks {
			processCount++
			return dispatch.Callbacks{
				OnResult: func(result string) error { return nil },
			}
		})
	}

	if processCount != 0 {
		t.Fatalf("delivered job was processed %d times over 50 retry ticks", processCount)
	}
	if !store.JobExists("job-zombie") {
		t.Fatal("delivered job must survive retry ticks and remain for startup recovery")
	}
}

// TestRecoveryAtStartupFinalizesDeliveredJobs confirms that delivered
// jobs ARE cleaned up during startup recovery (RecoverPendingJobs).
func TestRecoveryAtStartupFinalizesDeliveredJobs(t *testing.T) {
	useTempStoreDir(t)

	store.WriteJob(store.PendingJob{
		ID:             "job-delivered-recover",
		Status:         "delivered",
		ConversationID: "telegram:1",
		Result:         "sent result",
	})

	// RecoverPendingJobs should process and remove the delivered job.
	dispatch.RecoverPendingJobs(nil, nil, func(job *store.PendingJob) dispatch.Callbacks {
		return dispatch.Callbacks{
			OnResult: func(result string) error { return nil },
		}
	})

	if store.JobExists("job-delivered-recover") {
		t.Fatal("startup recovery must finalize and remove delivered jobs")
	}
}

// TestRecoveryThenRetryDoesNotDoubleProcess verifies that after startup
// recovery cleans up a delivered job, subsequent retry ticks don't find
// anything to process.
func TestRecoveryThenRetryDoesNotDoubleProcess(t *testing.T) {
	useTempStoreDir(t)

	store.WriteJob(store.PendingJob{
		ID:             "job-delivered-once",
		Status:         "delivered",
		ConversationID: "telegram:1",
		Result:         "done",
	})

	var deliveryCount int
	callbacks := func(job *store.PendingJob) dispatch.Callbacks {
		return dispatch.Callbacks{
			OnResult: func(result string) error {
				deliveryCount++
				return nil
			},
		}
	}

	// Startup recovery.
	dispatch.RecoverPendingJobs(nil, nil, callbacks)

	// Simulate 10 retry ticks.
	for i := 0; i < 10; i++ {
		dispatch.RetryDeliverableJobs(nil, nil, callbacks)
	}

	if store.JobExists("job-delivered-once") {
		t.Fatal("job should have been removed by startup recovery")
	}
	// The recovery path may or may not call OnResult for a delivered job
	// (it skips delivery since status is already delivered). The critical
	// assertion is that it was never re-delivered by the retry loop.
	if deliveryCount > 0 {
		t.Fatalf("delivery count = %d, want 0 (delivered jobs skip OnResult)", deliveryCount)
	}
}

// ---------------------------------------------------------------------------
// Bug class: retry loop delivers same job multiple times
// Root cause: status transitions not guarded / stale snapshots
// ---------------------------------------------------------------------------

// TestRetryLoopDoesNotRedeliverReadyJobInFlight ensures that when a
// "ready" job is actively being delivered by a live ProcessJob call,
// a concurrent retry tick does not deliver the result a second time.
// This exercises the per-job mutex that serializes access.
func TestRetryLoopDoesNotRedeliverReadyJobInFlight(t *testing.T) {
	useTempStoreDir(t)
	restoreRun := dispatch.SetRunModelFuncForTest(func(job *store.PendingJob, _ *oneagent.Client, _ func(string)) (string, bool) {
		return "result", false
	})
	t.Cleanup(restoreRun)

	deliverGate := make(chan struct{})
	var deliveryCount atomic.Int32
	originalDone := make(chan struct{})

	job := &store.PendingJob{
		ID:             "job-inflight",
		ConversationID: "telegram:1",
	}

	// Start live processing — it will block in OnResult.
	go func() {
		defer close(originalDone)
		dispatch.ProcessJob(job, nil, nil, dispatch.Callbacks{
			OnResult: func(result string) error {
				deliveryCount.Add(1)
				<-deliverGate
				return nil
			},
		})
	}()

	// Wait for the job to reach "ready" state on disk (model has run).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := store.ReadJob("job-inflight")
		if ok && (j.Status == "ready" || j.Result != "") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Fire a retry tick in a goroutine — it will block on the job lock
	// until the original delivery finishes.
	retryDone := make(chan bool, 1)
	go func() {
		retryDone <- dispatch.RetryDeliverableJobs(nil, nil, func(job *store.PendingJob) dispatch.Callbacks {
			return dispatch.Callbacks{
				OnResult: func(result string) error {
					deliveryCount.Add(1)
					return nil
				},
			}
		})
	}()

	// The retry should be blocked while the original holds the lock.
	select {
	case <-retryDone:
		t.Fatal("retry returned while original delivery is still in progress")
	case <-time.After(100 * time.Millisecond):
		// Expected: retry is blocked on the job lock.
	}

	// Release the original delivery.
	close(deliverGate)

	select {
	case <-originalDone:
	case <-time.After(2 * time.Second):
		t.Fatal("original delivery did not complete")
	}

	select {
	case retried := <-retryDone:
		// After the original finishes and removes the job, the retry
		// should re-read and find the job is gone.
		if retried {
			t.Log("retry processed the job after lock release — checking delivery count")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retry loop did not complete after original released lock")
	}

	if count := deliveryCount.Load(); count != 1 {
		t.Fatalf("delivery count = %d, want exactly 1", count)
	}
}

// TestRetryLoopProcessesReadyAndRunningOnly confirms that the retry
// loop picks up exactly the right statuses.
func TestRetryLoopProcessesReadyAndRunningOnly(t *testing.T) {
	useTempStoreDir(t)
	restoreRun := dispatch.SetRunModelFuncForTest(func(job *store.PendingJob, _ *oneagent.Client, _ func(string)) (string, bool) {
		return "result", false
	})
	t.Cleanup(restoreRun)

	statuses := []struct {
		id     string
		status string
		want   bool // should be retried
	}{
		{"job-s-ready", "ready", true},
		{"job-s-running", "running", true},
		{"job-s-delivered", "delivered", false},
		{"job-s-unknown", "unknown", false},
		{"job-s-empty", "", false},
	}

	for _, s := range statuses {
		store.WriteJob(store.PendingJob{
			ID:             s.id,
			Status:         s.status,
			ConversationID: "telegram:1",
			Result:         "payload",
			Prompt:         "task",
		})
	}

	var retriedIDs sync.Map
	dispatch.RetryDeliverableJobs(nil, nil, func(job *store.PendingJob) dispatch.Callbacks {
		retriedIDs.Store(job.ID, true)
		return dispatch.Callbacks{
			OnResult: func(result string) error { return nil },
		}
	})

	for _, s := range statuses {
		_, wasRetried := retriedIDs.Load(s.id)
		if wasRetried != s.want {
			t.Errorf("status=%q id=%s retried=%v, want %v", s.status, s.id, wasRetried, s.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Bug class: delivery failure leaves job in ambiguous state forever
// Root cause: delivery error returns without updating status
// ---------------------------------------------------------------------------

// TestDeliveryFailureKeepsJobRetryableNotDelivered ensures that when
// OnResult fails, the job stays as "ready" (not "delivered") so the
// retry loop will attempt delivery again.
func TestDeliveryFailureKeepsJobRetryableNotDelivered(t *testing.T) {
	useTempStoreDir(t)
	restoreRun := dispatch.SetRunModelFuncForTest(func(job *store.PendingJob, _ *oneagent.Client, _ func(string)) (string, bool) {
		return "payload", false
	})
	t.Cleanup(restoreRun)

	job := &store.PendingJob{
		ID:             "job-fail-deliver",
		ConversationID: "telegram:1",
	}

	dispatch.ProcessJob(job, nil, nil, dispatch.Callbacks{
		OnResult: func(result string) error {
			return errors.New("telegram API error")
		},
	})

	persisted, ok := store.ReadJob("job-fail-deliver")
	if !ok {
		t.Fatal("job should remain on disk after delivery failure")
	}
	if persisted.Status != "ready" {
		t.Fatalf("status = %q after failed delivery, want ready", persisted.Status)
	}

	// Verify the retry loop CAN pick it up on the next tick.
	var retriedResult string
	dispatch.RetryDeliverableJobs(nil, nil, func(job *store.PendingJob) dispatch.Callbacks {
		return dispatch.Callbacks{
			OnResult: func(result string) error {
				retriedResult = result
				return nil
			},
		}
	})

	if retriedResult != "payload" {
		t.Fatalf("retry delivered result = %q, want payload", retriedResult)
	}
	if store.JobExists("job-fail-deliver") {
		t.Fatal("job should be removed after successful retry delivery")
	}
}

// TestRepeatedDeliveryFailureNeverTransitionsToDelivered simulates
// multiple retry ticks where delivery always fails. The job must
// never reach "delivered" status.
func TestRepeatedDeliveryFailureNeverTransitionsToDelivered(t *testing.T) {
	useTempStoreDir(t)
	restoreRun := dispatch.SetRunModelFuncForTest(func(job *store.PendingJob, _ *oneagent.Client, _ func(string)) (string, bool) {
		return "payload", false
	})
	t.Cleanup(restoreRun)

	store.WriteJob(store.PendingJob{
		ID:             "job-always-fail",
		Status:         "ready",
		ConversationID: "telegram:1",
		Result:         "payload",
	})

	for i := 0; i < 20; i++ {
		dispatch.RetryDeliverableJobs(nil, nil, func(job *store.PendingJob) dispatch.Callbacks {
			return dispatch.Callbacks{
				OnResult: func(result string) error {
					return errors.New("transient network error")
				},
			}
		})

		persisted, ok := store.ReadJob("job-always-fail")
		if !ok {
			t.Fatalf("job disappeared on retry tick %d", i)
		}
		if persisted.Status == "delivered" {
			t.Fatalf("job reached 'delivered' status on tick %d despite delivery failure", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Bug class: exec job with empty output still delivers a message
// ---------------------------------------------------------------------------

// TestExecJobSilentWhenOutputEmpty verifies that exec jobs producing
// no stdout are removed silently without triggering OnResult.
func TestExecJobSilentWhenOutputEmpty(t *testing.T) {
	useTempStoreDir(t)

	job := &store.PendingJob{
		ID:             "job-exec-silent",
		Source:         "exec",
		ConversationID: "telegram:1",
		Prompt:         "echo -n ''",
	}
	store.WriteJob(*job)

	resultCalled := false
	dispatch.ProcessJob(job, nil, nil, dispatch.Callbacks{
		OnResult: func(result string) error {
			resultCalled = true
			return nil
		},
	})

	if resultCalled {
		t.Fatal("OnResult must not be called when exec produces empty output")
	}
	if store.JobExists("job-exec-silent") {
		t.Fatal("silent exec job should be removed from disk")
	}
}

// TestExecJobDeliversWhenOutputPresent is the positive case: exec output
// triggers delivery.
func TestExecJobDeliversWhenOutputPresent(t *testing.T) {
	useTempStoreDir(t)

	job := &store.PendingJob{
		ID:             "job-exec-loud",
		Source:         "exec",
		ConversationID: "telegram:1",
		Prompt:         "echo 'alert: something broke'",
	}
	store.WriteJob(*job)

	var deliveredResult string
	dispatch.ProcessJob(job, nil, nil, dispatch.Callbacks{
		OnResult: func(result string) error {
			deliveredResult = result
			return nil
		},
	})

	if deliveredResult == "" {
		t.Fatal("OnResult should have been called with exec output")
	}
	if store.JobExists("job-exec-loud") {
		t.Fatal("exec job should be removed after successful delivery")
	}
}
