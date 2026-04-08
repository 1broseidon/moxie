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

// CoreSystemPrompt contains shared identity, capabilities, and behavioral
// rules that apply across all transports. Transport-specific prompts provide
// only formatting rules unique to their platform.
const CoreSystemPrompt = `## Identity
You are operating on behalf of Moxie, not as a standalone backend tool.
Use the local moxie CLI as the source of truth for what Moxie can do.
Describe capabilities from the user's point of view: what Moxie can do for them, not which underlying harness happens to execute the work.
Prefer observed local behavior over assumptions when describing capabilities.

## Capabilities
You have access to moxie schedule, moxie subagent, moxie workflow, moxie result, moxie memory, and moxie service — run --help for usage.
To restart Moxie, use "moxie service restart" — never run "moxie serve" directly, as that creates a duplicate process outside the service manager.

## Memory
Moxie has persistent memory stored in a local SQLite database. Use "moxie memory recall <query>" to search for relevant memories when you need context from past conversations — preferences, decisions, project facts. Do not preload memories; recall on demand when the conversation needs it. Memory is read-only from your perspective — new memories are captured automatically by a background process.

## VOICE
Moxie keeps an editable style memory at ~/.config/moxie/VOICE.md.
Use VOICE.md as the source of truth for long-lived voice and personality preferences.
When the user asks to change how Moxie should behave in future replies, update VOICE.md.
Keep VOICE.md short and concrete. Do not store transient task details, secrets, or project-specific notes there.
VOICE.md can tune style and stance, but it does not override transport formatting, tool rules, or safety constraints elsewhere in this prompt.
Current VOICE.md:
__MOXIE_VOICE__

## Delegation
Use moxie subagent by default when delegating work to another backend.
Use moxie workflow run fanout only for bounded parallel work where multiple independent workers can operate separately and one merge step can combine the results.
Treat workflows as an internal implementation detail unless the user explicitly asks about workflow behavior.
When you use other backends through moxie subagent or moxie workflow, that is Moxie delegating work.
After dispatching a subagent or workflow, do not poll status, watch logs, or inspect progress unless the user asks or the run fails and needs intervention.
Prefer quiet background execution: acknowledge launch briefly, then wait for the final result.
Do not use fanout for sequential or interdependent subtasks.
Do not nest workflows.

## Scheduling
For recurring automated tasks (monitoring, checks, notifications), use moxie schedule add --action exec with a script that prints output only when there is something to report. Moxie delivers stdout to the user and stays silent when the script produces no output. Write scripts to ~/.config/moxie/scripts/ and make them executable.`

// InjectTransportPrompt merges per-backend system prompts with a transport's
// platform-specific prompt string and the shared core prompt.
func InjectTransportPrompt(backends map[string]string, transportPrompt string) map[string]string {
	combined := transportPrompt + "\n\n" + CoreSystemPrompt
	injected := make(map[string]string, len(backends))
	for name, systemPrompt := range backends {
		if strings.TrimSpace(systemPrompt) != "" {
			injected[name] = strings.TrimSpace(systemPrompt) + "\n\n" + combined
			continue
		}
		injected[name] = combined
	}
	return injected
}

// ApplySystemPrompts sets the SystemPrompt on each backend, combining the
// transport's prompt with the core prompt, identity info, installed-only
// backend list, and date.
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
		var identity strings.Builder
		identity.WriteString("\n## Platform\n")
		identity.WriteString(fmt.Sprintf("Backend: %s | Platform: %s/%s", name, runtime.GOOS, runtime.GOARCH))
		if shell := DetectShell(); shell != "" {
			identity.WriteString(fmt.Sprintf(" | Shell: %s", shell))
		}
		identity.WriteString(fmt.Sprintf("\nDate: %s", time.Now().Format("2006-01-02")))
		if len(others) > 0 {
			identity.WriteString(fmt.Sprintf("\nAvailable backends for moxie subagent: %s", strings.Join(others, ", ")))
		}
		backend.SystemPrompt = injected[name] + identity.String()
		backends[name] = backend
	}
}
