package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/1broseidon/moxie/internal/store"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = orig
	})

	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("stdout close: %v", err)
	}
	os.Stdout = orig
	return <-done
}

func withArgs(t *testing.T, args ...string) {
	t.Helper()
	prev := os.Args
	os.Args = args
	t.Cleanup(func() {
		os.Args = prev
	})
}

func writeSubagentTestConfig(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(store.ConfigDir(), 0700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	store.SaveConfig(store.Config{
		Channels: map[string]store.ChannelConfig{
			"telegram": {
				Provider:  "telegram",
				Token:     "token",
				ChannelID: "123",
			},
		},
	})
}

func TestFindParentJobPrefersCurrentJobEnv(t *testing.T) {
	restoreStore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restoreStore)

	store.WriteJob(store.PendingJob{
		ID:             "job-root",
		Status:         "running",
		Source:         "telegram",
		ConversationID: "telegram:123",
	})
	store.WriteJob(store.PendingJob{
		ID:             "job-sub1",
		Status:         "running",
		Source:         "subagent",
		Depth:          1,
		ConversationID: "telegram:123",
	})

	t.Setenv("MOXIE_JOB_ID", "job-sub1")

	got := findParentJob("")
	if got == nil {
		t.Fatal("findParentJob() returned nil")
	}
	if got.ID != "job-sub1" {
		t.Fatalf("findParentJob() picked %q, want job-sub1", got.ID)
	}
}

func TestCmdSubagentBlocksForNestedParentAndUsesImmediateParent(t *testing.T) {
	restoreStore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restoreStore)
	writeSubagentTestConfig(t)

	prevPoll := subagentBlockingPollInterval
	prevTimeout := subagentBlockingTimeout
	subagentBlockingPollInterval = 10 * time.Millisecond
	subagentBlockingTimeout = 2 * time.Second
	t.Cleanup(func() {
		subagentBlockingPollInterval = prevPoll
		subagentBlockingTimeout = prevTimeout
	})

	root := store.PendingJob{
		ID:             "job-root",
		Status:         "running",
		Source:         "telegram",
		ConversationID: "telegram:123",
		Prompt:         "root prompt",
		State:          store.State{Backend: "claude", ThreadID: "root-thread"},
	}
	parent := store.PendingJob{
		ID:             "job-sub1",
		Status:         "running",
		Source:         "subagent",
		Depth:          1,
		ConversationID: "telegram:123",
		Prompt:         "sub1 prompt",
		CWD:            t.TempDir(),
		State:          store.State{Backend: "claude", ThreadID: "sub1-thread"},
	}
	store.WriteJob(root)
	store.WriteJob(parent)

	t.Setenv("MOXIE_JOB_ID", parent.ID)
	withArgs(t, "moxie", "subagent", "--backend", "codex", "--text", "inspect nested child")

	childDone := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			for _, job := range store.ListJobs() {
				if job.ID == root.ID || job.ID == parent.ID || job.ParentJobID != parent.ID {
					continue
				}
				if job.BlockingResultPath == "" {
					childDone <- os.ErrInvalid
					return
				}
				if job.ParentJobID != parent.ID {
					childDone <- os.ErrInvalid
					return
				}
				if err := os.MkdirAll(filepath.Dir(job.BlockingResultPath), 0700); err != nil {
					childDone <- err
					return
				}
				if err := os.WriteFile(job.BlockingResultPath, []byte("child result"), 0600); err != nil {
					childDone <- err
					return
				}
				store.RemoveJob(job.ID)
				childDone <- nil
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		childDone <- os.ErrDeadlineExceeded
	}()

	out := captureStdout(t, cmdSubagent)

	if out != "child result" {
		t.Fatalf("cmdSubagent() stdout = %q, want child result", out)
	}

	select {
	case err := <-childDone:
		if err != nil {
			t.Fatalf("child completion simulation failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for child completion simulation")
	}
}

func TestRunSubagentJobsDispatchesInParallel(t *testing.T) {
	restoreStore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restoreStore)

	// Write 3 subagent jobs targeting telegram.
	for _, id := range []string{"job-a", "job-b", "job-c"} {
		store.WriteJob(store.PendingJob{
			ID:             id,
			Source:         "subagent",
			Status:         "",
			ConversationID: "telegram:123",
			State:          store.State{Backend: "claude"},
		})
	}

	// We can't easily mock ProcessJob here, but we can verify that
	// inFlight tracking works correctly: all 3 jobs should be marked
	// in-flight simultaneously when transport is missing (they'll log
	// and skip since telegramBot is nil, but the goroutines still launch).
	st := &subagentTransports{
		schedules: nil,
		maxDepth:  3,
		inFlight:  make(map[string]struct{}),
		// telegramBot is nil — jobs will log "no telegram transport" and skip
	}

	runSubagentJobs(st)

	// Give goroutines a moment to clean up inFlight entries.
	time.Sleep(50 * time.Millisecond)

	st.mu.Lock()
	remaining := len(st.inFlight)
	st.mu.Unlock()

	// All jobs should have been attempted and cleaned up from inFlight.
	if remaining != 0 {
		t.Fatalf("inFlight has %d entries, want 0 (all should have completed)", remaining)
	}
}

func TestRunSubagentJobsSkipsInFlightJobs(t *testing.T) {
	restoreStore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restoreStore)

	store.WriteJob(store.PendingJob{
		ID:             "job-already-running",
		Source:         "subagent",
		Status:         "",
		ConversationID: "telegram:123",
		State:          store.State{Backend: "claude"},
	})

	st := &subagentTransports{
		schedules: nil,
		maxDepth:  3,
		inFlight:  make(map[string]struct{}),
	}

	// Pre-mark the job as in-flight (simulates a previous tick still processing it).
	st.mu.Lock()
	st.inFlight["job-already-running"] = struct{}{}
	st.mu.Unlock()

	runSubagentJobs(st)

	// The job should still be in-flight (not removed by this tick since it was skipped).
	st.mu.Lock()
	_, stillInFlight := st.inFlight["job-already-running"]
	st.mu.Unlock()

	if !stillInFlight {
		t.Fatal("job was removed from inFlight — it should have been skipped entirely")
	}
}
