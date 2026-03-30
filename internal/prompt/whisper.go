package prompt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WhisperInfo holds detection results for an available whisper transcription tool.
type WhisperInfo struct {
	Available bool
	Binary    string // absolute path to detected binary
	Variant   string // "cpp", "python", or "faster"
	ModelPath string // path to a model file, if found
}

var (
	whisperOnce   sync.Once
	cachedWhisper WhisperInfo
)

// WarmWhisper pre-runs whisper detection in the background so that the first
// voice message is fast. Safe to call multiple times; detection runs at most once.
func WarmWhisper() {
	whisperOnce.Do(runWhisperDetection)
}

// GetWhisperInfo returns cached whisper detection results, running detection
// on first call. Results are cached for the process lifetime.
func GetWhisperInfo() WhisperInfo {
	whisperOnce.Do(runWhisperDetection)
	return cachedWhisper
}

func runWhisperDetection() {
	// whisper.cpp variants — check most-specific names first
	for _, name := range []string{"whisper-cpp", "whisper-cli", "whisper.cpp"} {
		if path, err := exec.LookPath(name); err == nil {
			cachedWhisper = WhisperInfo{
				Available: true,
				Binary:    path,
				Variant:   "cpp",
				ModelPath: findWhisperModel("cpp"),
			}
			return
		}
	}

	// faster-whisper
	if path, err := exec.LookPath("faster-whisper"); err == nil {
		cachedWhisper = WhisperInfo{
			Available: true,
			Binary:    path,
			Variant:   "faster",
			ModelPath: findWhisperModel("faster"),
		}
		return
	}

	// Python openai-whisper — verify the 'whisper' binary is actually openai-whisper
	// (not some other tool named whisper)
	if path, err := exec.LookPath("whisper"); err == nil && isPythonWhisper(path) {
		cachedWhisper = WhisperInfo{
			Available: true,
			Binary:    path,
			Variant:   "python",
			ModelPath: findWhisperModel("python"),
		}
		return
	}
}

// isPythonWhisper checks if the given binary path is the openai-whisper package
// by examining its --help output for known signatures.
func isPythonWhisper(binaryPath string) bool {
	cmd := exec.Command(binaryPath, "--help") //nolint:gosec
	cmd.WaitDelay = 3 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	help := strings.ToLower(string(out))
	return strings.Contains(help, "openai") ||
		strings.Contains(help, "audio_path") ||
		strings.Contains(help, "model_dir") ||
		strings.Contains(help, "word_timestamps")
}

// findWhisperModel looks for model files in common locations for the given variant.
// Returns the path to the first model file found, or "" if none.
func findWhisperModel(variant string) string {
	home, _ := os.UserHomeDir()
	var searchDirs []string

	switch variant {
	case "cpp":
		searchDirs = []string{
			filepath.Join(home, ".local", "share", "whisper"),
			"/usr/local/share/whisper",
			"/usr/share/whisper",
		}
	case "python":
		searchDirs = []string{
			filepath.Join(home, ".cache", "whisper"),
		}
	case "faster":
		searchDirs = []string{
			filepath.Join(home, ".cache", "huggingface"),
			filepath.Join(home, ".cache", "faster_whisper"),
		}
	}

	for _, dir := range searchDirs {
		if path := findFirstModelFile(dir, variant); path != "" {
			return path
		}
	}
	return ""
}

// findFirstModelFile scans a directory for the first recognisable model file
// for the given variant. Does not recurse.
func findFirstModelFile(dir, variant string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		name := entry.Name()
		switch variant {
		case "cpp":
			// ggml-*.bin (e.g. ggml-base.en.bin)
			if strings.HasPrefix(name, "ggml-") && strings.HasSuffix(name, ".bin") {
				return filepath.Join(dir, name)
			}
		case "python":
			// PyTorch checkpoints (.pt) or arbitrary .bin blobs
			if strings.HasSuffix(name, ".pt") || strings.HasSuffix(name, ".bin") {
				return filepath.Join(dir, name)
			}
		case "faster":
			// faster-whisper stores models as sub-directories
			if entry.IsDir() {
				return filepath.Join(dir, name)
			}
		}
	}
	return ""
}
