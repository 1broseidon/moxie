package prompt

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// AudioMeta holds basic metadata about an audio file.
type AudioMeta struct {
	DurationSecs float64 // 0 means unknown
	Format       string  // human-readable format label, e.g. "opus/ogg"
}

// FormatAudioPrompt builds an enriched prompt for voice/audio messages.
// It includes file path, duration, format, and any detected transcription tool.
// Whisper detection is cached globally; call WarmWhisper() at startup to
// pre-populate the cache before the first message arrives.
func FormatAudioPrompt(kind, path, caption, fallback string) string {
	meta := probeAudio(path)
	return BuildAudioPrompt(kind, path, caption, fallback, meta, GetWhisperInfo())
}

// BuildAudioPrompt is the pure formatting function used by FormatAudioPrompt.
// It accepts pre-computed AudioMeta and WhisperInfo so tests can exercise it
// without side effects.
func BuildAudioPrompt(kind, path, caption, fallback string, meta AudioMeta, whisper WhisperInfo) string {
	var sb strings.Builder

	// First line: "User sent <kind>: <path>"
	sb.WriteString("User sent ")
	sb.WriteString(kind)
	sb.WriteString(": ")
	sb.WriteString(path)

	// Second line: duration and/or format
	var metaParts []string
	if meta.DurationSecs > 0 {
		metaParts = append(metaParts, fmt.Sprintf("Duration: %.1fs", meta.DurationSecs))
	}
	if meta.Format != "" {
		metaParts = append(metaParts, "Format: "+meta.Format)
	}
	if len(metaParts) > 0 {
		sb.WriteString("\n")
		sb.WriteString(strings.Join(metaParts, " | "))
	}

	// Transcription tool if detected
	if whisper.Available {
		variantLabel := variantDisplayName(whisper.Variant)
		sb.WriteString(fmt.Sprintf("\nTranscription tool detected: %s (%s)", whisper.Binary, variantLabel))
		if whisper.ModelPath != "" {
			sb.WriteString("\nModel: ")
			sb.WriteString(whisper.ModelPath)
		}
	}

	// Caption or fallback request
	if caption != "" {
		sb.WriteString("\nCaption: ")
		sb.WriteString(caption)
	} else {
		sb.WriteString("\nRequest: ")
		sb.WriteString(fallback)
	}

	return sb.String()
}

func variantDisplayName(variant string) string {
	switch variant {
	case "cpp":
		return "whisper.cpp"
	case "python":
		return "openai-whisper"
	case "faster":
		return "faster-whisper"
	default:
		if variant != "" {
			return variant
		}
		return "whisper"
	}
}

// probeAudio collects metadata for the given audio file path.
func probeAudio(path string) AudioMeta {
	return AudioMeta{
		DurationSecs: probeDuration(path),
		Format:       DetectAudioFormat(path),
	}
}

// probeDuration uses ffprobe to extract the duration of an audio file.
// Returns 0 if ffprobe is not available or the probe fails.
func probeDuration(path string) float64 {
	ffprobeCmd, err := exec.LookPath("ffprobe")
	if err != nil {
		return 0
	}
	cmd := exec.Command(ffprobeCmd, //nolint:gosec
		"-v", "quiet",
		"-show_entries", "format=duration",
		"-of", "csv=p=0",
		path,
	)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return secs
}

// DetectAudioFormat returns a human-readable format label based on the file extension.
func DetectAudioFormat(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".oga", ".ogg":
		return "opus/ogg"
	case ".mp3":
		return "mp3"
	case ".wav":
		return "wav"
	case ".m4a":
		return "m4a"
	case ".flac":
		return "flac"
	case ".webm":
		return "webm"
	case ".aac":
		return "aac"
	case ".opus":
		return "opus"
	default:
		trimmed := strings.TrimPrefix(ext, ".")
		return trimmed // may be empty for files without extension
	}
}
