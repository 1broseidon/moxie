package prompt

import (
	"os"
	"strings"
	"testing"

	"github.com/1broseidon/moxie/internal/store"
)

func TestLoadVoiceCreatesDefaultFile(t *testing.T) {
	cleanup := store.SetConfigDir(t.TempDir())
	defer cleanup()

	got, err := LoadVoice()
	if err != nil {
		t.Fatalf("LoadVoice() err = %v", err)
	}
	if strings.TrimSpace(got) != strings.TrimSpace(DefaultVoice()) {
		t.Fatalf("LoadVoice() = %q, want default voice", got)
	}
	if _, err := os.Stat(VoicePath()); err != nil {
		t.Fatalf("VOICE.md was not created: %v", err)
	}
}

func TestEnsureVoiceFileDoesNotOverwrite(t *testing.T) {
	cleanup := store.SetConfigDir(t.TempDir())
	defer cleanup()

	if err := os.MkdirAll(store.ConfigDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll() err = %v", err)
	}
	want := "custom voice"
	if err := os.WriteFile(VoicePath(), []byte(want), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}

	if err := EnsureVoiceFile(); err != nil {
		t.Fatalf("EnsureVoiceFile() err = %v", err)
	}
	got, err := os.ReadFile(VoicePath())
	if err != nil {
		t.Fatalf("ReadFile() err = %v", err)
	}
	if string(got) != want {
		t.Fatalf("EnsureVoiceFile() overwrote existing file: got %q, want %q", string(got), want)
	}
}

func TestResolveDynamicSystemPromptInjectsCurrentVoice(t *testing.T) {
	cleanup := store.SetConfigDir(t.TempDir())
	defer cleanup()

	wantVoice := "# Moxie VOICE\n\ncustom vibe"
	if err := os.MkdirAll(store.ConfigDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll() err = %v", err)
	}
	if err := os.WriteFile(VoicePath(), []byte(wantVoice), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}

	got := ResolveDynamicSystemPrompt("before\n" + VoicePlaceholder + "\nafter")
	if strings.Contains(got, VoicePlaceholder) {
		t.Fatalf("ResolveDynamicSystemPrompt() did not replace placeholder: %q", got)
	}
	if !strings.Contains(got, wantVoice) {
		t.Fatalf("ResolveDynamicSystemPrompt() missing voice contents: %q", got)
	}
}

func TestResolveDynamicSystemPromptHandlesEmptyVoice(t *testing.T) {
	cleanup := store.SetConfigDir(t.TempDir())
	defer cleanup()

	if err := os.MkdirAll(store.ConfigDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll() err = %v", err)
	}
	if err := os.WriteFile(VoicePath(), []byte("\n\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() err = %v", err)
	}

	got := ResolveDynamicSystemPrompt(VoicePlaceholder)
	if got != "(VOICE.md is currently empty.)" {
		t.Fatalf("ResolveDynamicSystemPrompt() = %q, want empty marker", got)
	}
}

func TestFormatVoiceForPromptTruncatesLargeVoice(t *testing.T) {
	large := strings.Repeat("a", maxVoiceRunes+100)
	got := formatVoiceForPrompt(large)
	if !strings.Contains(got, "[VOICE truncated to the first 4000 characters for prompt injection.]") {
		t.Fatalf("formatVoiceForPrompt() missing truncation note: %q", got)
	}
	if len([]rune(got)) <= maxVoiceRunes {
		t.Fatalf("formatVoiceForPrompt() did not include truncation note")
	}
}
