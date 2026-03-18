package prompt

import (
	"strings"
	"testing"

	"github.com/1broseidon/oneagent"
)

func TestInjectSystemPrompt(t *testing.T) {
	backends := map[string]string{
		"claude": "  existing prompt  ",
		"pi":     "",
	}

	got := InjectSystemPrompt(backends)

	if !strings.HasPrefix(got["claude"], "existing prompt\n\n") {
		t.Fatalf("claude prompt = %q, want existing prompt prefix", got["claude"])
	}
	if !strings.Contains(got["claude"], TeleSystemPrompt) {
		t.Fatalf("claude prompt missing tele system prompt")
	}
	if got["pi"] != TeleSystemPrompt {
		t.Fatalf("pi prompt = %q, want TeleSystemPrompt", got["pi"])
	}
}

func TestApplySystemPrompt(t *testing.T) {
	backends := map[string]oneagent.Backend{
		"claude": {SystemPrompt: "base"},
		"pi":     {},
	}

	ApplySystemPrompt(backends)

	if !strings.HasPrefix(backends["claude"].SystemPrompt, "base\n\n") {
		t.Fatalf("claude system prompt = %q, want base prefix", backends["claude"].SystemPrompt)
	}
	if backends["pi"].SystemPrompt != TeleSystemPrompt {
		t.Fatalf("pi system prompt = %q, want TeleSystemPrompt", backends["pi"].SystemPrompt)
	}
}

func TestFormatMediaPrompt(t *testing.T) {
	got := FormatMediaPrompt("a photo", "/tmp/photo.jpg", "caption here", "fallback")
	want := "User sent a photo: /tmp/photo.jpg\nCaption: caption here"
	if got != want {
		t.Fatalf("FormatMediaPrompt() = %q, want %q", got, want)
	}

	got = FormatMediaPrompt("a file", "/tmp/doc.pdf", "", "User sent a file")
	want = "User sent a file: /tmp/doc.pdf\nRequest: User sent a file"
	if got != want {
		t.Fatalf("FormatMediaPrompt() fallback = %q, want %q", got, want)
	}
}
