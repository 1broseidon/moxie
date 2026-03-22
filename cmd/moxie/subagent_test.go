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
