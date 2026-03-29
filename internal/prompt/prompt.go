package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/1broseidon/oneagent"
)

func FormatMediaPrompt(kind, path, caption, fallbackRequest string) string {
	line := fmt.Sprintf("User sent %s: %s", kind, path)
	if caption != "" {
		return line + "\nCaption: " + caption
	}
	return line + "\nRequest: " + fallbackRequest
}

// DetectShell returns the current shell name, or "" if unknown.
func DetectShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return filepath.Base(s)
	}
	if runtime.GOOS == "windows" {
		if os.Getenv("PSModulePath") != "" {
			return "powershell"
		}
		return "cmd"
	}
	return ""
}

// InjectTransportPrompt merges per-backend system prompts with a transport's
// platform-specific prompt string.
func InjectTransportPrompt(backends map[string]string, transportPrompt string) map[string]string {
	injected := make(map[string]string, len(backends))
	for name, systemPrompt := range backends {
		if strings.TrimSpace(systemPrompt) != "" {
			injected[name] = strings.TrimSpace(systemPrompt) + "\n\n" + transportPrompt
			continue
		}
		injected[name] = transportPrompt
	}
	return injected
}

// ApplySystemPrompts sets the SystemPrompt on each backend, combining the
// transport's prompt with identity info, installed-only backend list, and date.
func ApplySystemPrompts(backends map[string]oneagent.Backend, transportPrompt string) {
	systemPrompts := make(map[string]string, len(backends))
	for name, backend := range backends {
		systemPrompts[name] = backend.SystemPrompt
	}

	injected := InjectTransportPrompt(systemPrompts, transportPrompt)

	var allNames []string
	for name := range backends {
		allNames = append(allNames, name)
	}
	sort.Strings(allNames)

	for name, backend := range backends {
		var others []string
		for _, n := range allNames {
			if n != name {
				if b, ok := backends[n]; ok {
					if _, found := oneagent.ResolveBackendProgram(b); !found {
						continue
					}
				}
				others = append(others, n)
			}
		}
		identity := fmt.Sprintf("\nYou are running on the %s backend. Platform: %s/%s", name, runtime.GOOS, runtime.GOARCH)
		if shell := DetectShell(); shell != "" {
			identity += fmt.Sprintf(", shell: %s", shell)
		}
		identity += fmt.Sprintf(".\nCurrent date: %s", time.Now().Format("2006-01-02"))
		if len(others) > 0 {
			identity += fmt.Sprintf(" Available backends for moxie subagent: %s.", strings.Join(others, ", "))
		}
		backend.SystemPrompt = injected[name] + identity
		backends[name] = backend
	}
}
