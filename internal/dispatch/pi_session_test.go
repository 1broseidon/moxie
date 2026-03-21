package dispatch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

func TestRunModelClearsMissingPiNativeSessionBeforeRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	storeDir := t.TempDir()
	client := &oneagent.Client{
		Store: oneagent.FilesystemStore{Dir: storeDir},
		Backends: map[string]oneagent.Backend{
			"pi": fakePIBackend(
				`printf '{"type":"done","result":"fresh","session":"new-session"}\n'`,
				`printf '{"type":"error","message":"resume should not run"}\n'; exit 1`,
			),
		},
	}

	thread := &oneagent.Thread{
		ID: "pi-preflight",
		Turns: []oneagent.Turn{
			{Role: "assistant", Backend: "pi", Content: "old reply"},
		},
		NativeSessions: map[string]string{"pi": "missing-session"},
	}
	if err := client.SaveThread(thread); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	job := &store.PendingJob{
		ID:     "job-preflight",
		Prompt: "say hi",
		CWD:    filepath.Join(home, "work"),
		State: store.State{
			Backend:  "pi",
			ThreadID: thread.ID,
		},
	}
	if err := os.MkdirAll(job.CWD, 0o700); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	got, interrupted := RunModel(job, client, nil)
	if interrupted {
		t.Fatal("RunModel() interrupted, want false")
	}
	if got != "fresh" {
		t.Fatalf("RunModel() = %q, want fresh", got)
	}

	repaired, err := client.LoadThread(thread.ID)
	if err != nil {
		t.Fatalf("load repaired thread: %v", err)
	}
	if repaired.NativeSessions["pi"] != "new-session" {
		t.Fatalf("native session = %q, want new-session", repaired.NativeSessions["pi"])
	}
}

func TestRunModelRetriesPiOnceOnMissingNativeSessionError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	storeDir := t.TempDir()
	client := &oneagent.Client{
		Store: oneagent.FilesystemStore{Dir: storeDir},
		Backends: map[string]oneagent.Backend{
			"pi": fakePIBackend(
				`printf '{"type":"done","result":"fresh","session":"new-session"}\n'`,
				`printf '{"type":"error","message":"session not found"}\n'; exit 1`,
			),
		},
	}

	thread := &oneagent.Thread{
		ID: "pi-retry",
		Turns: []oneagent.Turn{
			{Role: "assistant", Backend: "pi", Content: "old reply"},
		},
		NativeSessions: map[string]string{"pi": "live-session"},
	}
	if err := client.SaveThread(thread); err != nil {
		t.Fatalf("save thread: %v", err)
	}

	cwd := filepath.Join(home, "work")
	sessionFile := filepath.Join(piSessionDir(cwd), "2026-03-20T13-00-00-000Z_live-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(sessionFile, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	job := &store.PendingJob{
		ID:     "job-retry",
		Prompt: "say hi",
		CWD:    cwd,
		State: store.State{
			Backend:  "pi",
			ThreadID: thread.ID,
		},
	}
	if err := os.MkdirAll(job.CWD, 0o700); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	got, interrupted := RunModel(job, client, nil)
	if interrupted {
		t.Fatal("RunModel() interrupted, want false")
	}
	if got != "fresh" {
		t.Fatalf("RunModel() = %q, want fresh", got)
	}

	repaired, err := client.LoadThread(thread.ID)
	if err != nil {
		t.Fatalf("load repaired thread: %v", err)
	}
	if repaired.NativeSessions["pi"] != "new-session" {
		t.Fatalf("native session = %q, want new-session", repaired.NativeSessions["pi"])
	}
}

func TestRunModelNormalizesSyntheticEmptyResult(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	client := &oneagent.Client{
		Backends: map[string]oneagent.Backend{
			"pi": fakePIBackend(
				`printf '{"type":"done","result":"Done — nothing to report.","session":"sess-1"}\n'`,
				`printf '{"type":"error","message":"resume should not run"}\n'; exit 1`,
			),
		},
	}

	job := &store.PendingJob{
		ID:     "job-empty-result",
		Prompt: "say hi",
		CWD:    filepath.Join(home, "work"),
		State: store.State{
			Backend:  "pi",
			ThreadID: "pi-empty-result",
		},
	}
	if err := os.MkdirAll(job.CWD, 0o700); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	got, interrupted := RunModel(job, client, nil)
	if interrupted {
		t.Fatal("RunModel() interrupted, want false")
	}
	if got != "" {
		t.Fatalf("RunModel() = %q, want empty", got)
	}
}

func fakePIBackend(cmd, resume string) oneagent.Backend {
	return oneagent.Backend{
		Cmd:         []string{"sh", "-c", cmd},
		ResumeCmd:   []string{"sh", "-c", resume},
		Format:      "jsonl",
		Result:      "result",
		ResultWhen:  "type=done",
		Session:     "session",
		SessionWhen: "type=done",
		Error:       "message",
		ErrorWhen:   "type=error",
	}
}
