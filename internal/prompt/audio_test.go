package prompt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectAudioFormat(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/tmp/voice.oga", "opus/ogg"},
		{"/tmp/audio.ogg", "opus/ogg"},
		{"/tmp/audio.mp3", "mp3"},
		{"/tmp/audio.wav", "wav"},
		{"/tmp/audio.m4a", "m4a"},
		{"/tmp/audio.flac", "flac"},
		{"/tmp/audio.webm", "webm"},
		{"/tmp/audio.aac", "aac"},
		{"/tmp/audio.opus", "opus"},
		{"/tmp/audio.xyz", "xyz"},
		{"/tmp/noext", ""},
	}
	for _, tt := range tests {
		got := DetectAudioFormat(tt.path)
		if got != tt.want {
			t.Errorf("DetectAudioFormat(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestBuildAudioPrompt_NoMetadata(t *testing.T) {
	got := BuildAudioPrompt("a voice message", "/tmp/voice.oga", "", "User sent a voice message",
		AudioMeta{}, WhisperInfo{})
	want := "User sent a voice message: /tmp/voice.oga\nRequest: User sent a voice message"
	if got != want {
		t.Errorf("BuildAudioPrompt (no metadata) =\n%q\nwant:\n%q", got, want)
	}
}

func TestBuildAudioPrompt_WithDurationAndFormat(t *testing.T) {
	meta := AudioMeta{DurationSecs: 10.8, Format: "opus/ogg"}
	got := BuildAudioPrompt("a voice message", "/tmp/voice.oga", "", "User sent a voice message",
		meta, WhisperInfo{})
	want := "User sent a voice message: /tmp/voice.oga\nDuration: 10.8s | Format: opus/ogg\nRequest: User sent a voice message"
	if got != want {
		t.Errorf("BuildAudioPrompt (with meta) =\n%q\nwant:\n%q", got, want)
	}
}

func TestBuildAudioPrompt_WithWhisperCpp(t *testing.T) {
	meta := AudioMeta{DurationSecs: 10.8, Format: "opus/ogg"}
	whisper := WhisperInfo{
		Available: true,
		Binary:    "/usr/local/bin/whisper-cpp",
		Variant:   "cpp",
		ModelPath: "/home/user/.local/share/whisper/ggml-base.en.bin",
	}
	got := BuildAudioPrompt("a voice message", "/tmp/voice.oga", "", "User sent a voice message",
		meta, whisper)
	want := "User sent a voice message: /tmp/voice.oga\n" +
		"Duration: 10.8s | Format: opus/ogg\n" +
		"Transcription tool detected: /usr/local/bin/whisper-cpp (whisper.cpp)\n" +
		"Model: /home/user/.local/share/whisper/ggml-base.en.bin\n" +
		"Request: User sent a voice message"
	if got != want {
		t.Errorf("BuildAudioPrompt (whisper.cpp) =\n%q\nwant:\n%q", got, want)
	}
}

func TestBuildAudioPrompt_WithWhisperNoModel(t *testing.T) {
	meta := AudioMeta{DurationSecs: 5.0, Format: "mp3"}
	whisper := WhisperInfo{
		Available: true,
		Binary:    "/usr/bin/whisper",
		Variant:   "python",
		ModelPath: "",
	}
	got := BuildAudioPrompt("a voice message", "/tmp/audio.mp3", "", "User sent a voice message",
		meta, whisper)
	want := "User sent a voice message: /tmp/audio.mp3\n" +
		"Duration: 5.0s | Format: mp3\n" +
		"Transcription tool detected: /usr/bin/whisper (openai-whisper)\n" +
		"Request: User sent a voice message"
	if got != want {
		t.Errorf("BuildAudioPrompt (python whisper, no model) =\n%q\nwant:\n%q", got, want)
	}
}

func TestBuildAudioPrompt_WithCaption(t *testing.T) {
	got := BuildAudioPrompt("a voice message", "/tmp/voice.oga", "transcribe please", "User sent a voice message",
		AudioMeta{DurationSecs: 3.2, Format: "opus/ogg"}, WhisperInfo{})
	want := "User sent a voice message: /tmp/voice.oga\n" +
		"Duration: 3.2s | Format: opus/ogg\n" +
		"Caption: transcribe please"
	if got != want {
		t.Errorf("BuildAudioPrompt (with caption) =\n%q\nwant:\n%q", got, want)
	}
}

func TestBuildAudioPrompt_FasterWhisper(t *testing.T) {
	whisper := WhisperInfo{
		Available: true,
		Binary:    "/usr/local/bin/faster-whisper",
		Variant:   "faster",
		ModelPath: "/home/user/.cache/huggingface/whisper-large-v3",
	}
	got := BuildAudioPrompt("a voice message", "/tmp/voice.ogg", "", "fallback",
		AudioMeta{}, whisper)
	want := "User sent a voice message: /tmp/voice.ogg\n" +
		"Transcription tool detected: /usr/local/bin/faster-whisper (faster-whisper)\n" +
		"Model: /home/user/.cache/huggingface/whisper-large-v3\n" +
		"Request: fallback"
	if got != want {
		t.Errorf("BuildAudioPrompt (faster-whisper) =\n%q\nwant:\n%q", got, want)
	}
}

func TestBuildAudioPrompt_DurationOnly(t *testing.T) {
	// Format can be empty for unknown extensions
	meta := AudioMeta{DurationSecs: 7.5, Format: ""}
	got := BuildAudioPrompt("a voice message", "/tmp/voice.bin", "", "fallback",
		meta, WhisperInfo{})
	want := "User sent a voice message: /tmp/voice.bin\nDuration: 7.5s\nRequest: fallback"
	if got != want {
		t.Errorf("BuildAudioPrompt (duration only) =\n%q\nwant:\n%q", got, want)
	}
}

func TestVariantDisplayName(t *testing.T) {
	tests := []struct {
		variant string
		want    string
	}{
		{"cpp", "whisper.cpp"},
		{"python", "openai-whisper"},
		{"faster", "faster-whisper"},
		{"custom", "custom"},
		{"", "whisper"},
	}
	for _, tt := range tests {
		got := variantDisplayName(tt.variant)
		if got != tt.want {
			t.Errorf("variantDisplayName(%q) = %q, want %q", tt.variant, got, tt.want)
		}
	}
}

func TestFindFirstModelFile_CppModel(t *testing.T) {
	dir := t.TempDir()
	// Create a fake ggml model file
	modelPath := filepath.Join(dir, "ggml-base.en.bin")
	if err := os.WriteFile(modelPath, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	got := findFirstModelFile(dir, "cpp")
	if got != modelPath {
		t.Errorf("findFirstModelFile (cpp) = %q, want %q", got, modelPath)
	}
}

func TestFindFirstModelFile_PythonModel(t *testing.T) {
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "large.pt")
	if err := os.WriteFile(modelPath, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	got := findFirstModelFile(dir, "python")
	if got != modelPath {
		t.Errorf("findFirstModelFile (python) = %q, want %q", got, modelPath)
	}
}

func TestFindFirstModelFile_FasterWhisperDir(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "whisper-large-v3")
	if err := os.Mkdir(subDir, 0700); err != nil {
		t.Fatal(err)
	}
	got := findFirstModelFile(dir, "faster")
	if got != subDir {
		t.Errorf("findFirstModelFile (faster) = %q, want %q", got, subDir)
	}
}

func TestFindFirstModelFile_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	got := findFirstModelFile(dir, "cpp")
	if got != "" {
		t.Errorf("findFirstModelFile (empty dir) = %q, want empty", got)
	}
}

func TestFindFirstModelFile_NonexistentDir(t *testing.T) {
	got := findFirstModelFile("/nonexistent/path/that/does/not/exist", "cpp")
	if got != "" {
		t.Errorf("findFirstModelFile (missing dir) = %q, want empty", got)
	}
}
