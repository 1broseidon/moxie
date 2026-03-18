package prompt

import (
	"testing"
)

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
