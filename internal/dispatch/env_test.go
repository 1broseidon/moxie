package dispatch

import (
	"os"
	"strings"
	"testing"

	"github.com/1broseidon/moxie/internal/prompt"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

func TestRunModelPropagatesMoxieJobIDEnv(t *testing.T) {
	client := &oneagent.Client{
		Backends: map[string]oneagent.Backend{
			"envtest": {
				Cmd:    []string{"sh", "-c", `printf '{"result":"%s"}' "$MOXIE_JOB_ID"`},
				Format: "json",
				Result: "result",
			},
		},
	}

	job := &store.PendingJob{
		ID:    "job-env-1",
		State: store.State{Backend: "envtest"},
	}

	result, interrupted := RunModel(job, client, nil)
	if interrupted {
		t.Fatal("RunModel reported interruption")
	}
	if result != "job-env-1" {
		t.Fatalf("result = %q, want job-env-1", result)
	}
}

func TestClientWithJobEnvInjectsVoicePrompt(t *testing.T) {
	cleanup := store.SetConfigDir(t.TempDir())
	defer cleanup()

	if err := os.WriteFile(prompt.VoicePath(), []byte("# Moxie VOICE\n\ncustom vibe"), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}

	backends := map[string]oneagent.Backend{
		"claude": {SystemPrompt: "before\n" + prompt.VoicePlaceholder + "\nafter"},
	}
	client := &oneagent.Client{Backends: backends}
	job := &store.PendingJob{State: store.State{Backend: "claude"}}

	got := clientWithJobEnv(client, job)
	if got == client {
		t.Fatal("clientWithJobEnv() returned original client; want cloned client with injected VOICE")
	}
	if strings.Contains(got.Backends["claude"].SystemPrompt, prompt.VoicePlaceholder) {
		t.Fatalf("SystemPrompt still contains placeholder: %q", got.Backends["claude"].SystemPrompt)
	}
	if !strings.Contains(got.Backends["claude"].SystemPrompt, "custom vibe") {
		t.Fatalf("SystemPrompt missing injected VOICE: %q", got.Backends["claude"].SystemPrompt)
	}
	if client.Backends["claude"].SystemPrompt != "before\n"+prompt.VoicePlaceholder+"\nafter" {
		t.Fatalf("original client prompt was mutated: %q", client.Backends["claude"].SystemPrompt)
	}
}

func TestClientWithJobEnvReloadsVoicePromptBetweenRuns(t *testing.T) {
	cleanup := store.SetConfigDir(t.TempDir())
	defer cleanup()

	client := &oneagent.Client{Backends: map[string]oneagent.Backend{
		"claude": {SystemPrompt: prompt.VoicePlaceholder},
	}}
	job := &store.PendingJob{State: store.State{Backend: "claude"}}

	if err := os.WriteFile(prompt.VoicePath(), []byte("first vibe"), 0o600); err != nil {
		t.Fatalf("WriteFile(first) err = %v", err)
	}
	first := clientWithJobEnv(client, job)

	if err := os.WriteFile(prompt.VoicePath(), []byte("second vibe"), 0o600); err != nil {
		t.Fatalf("WriteFile(second) err = %v", err)
	}
	second := clientWithJobEnv(client, job)

	if !strings.Contains(first.Backends["claude"].SystemPrompt, "first vibe") {
		t.Fatalf("first prompt = %q, want first vibe", first.Backends["claude"].SystemPrompt)
	}
	if !strings.Contains(second.Backends["claude"].SystemPrompt, "second vibe") {
		t.Fatalf("second prompt = %q, want second vibe", second.Backends["claude"].SystemPrompt)
	}
}
