package dispatch_test

import (
	"testing"

	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

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
