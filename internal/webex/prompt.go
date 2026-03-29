package webex

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/1broseidon/oneagent"
)

func detectShell() string {
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

const WebexSystemPrompt = `You are responding via a Webex direct-message bot. Format replies using simple Webex-friendly markdown.
Do not use HTML. Avoid markdown tables and overly complex formatting that may render poorly in Webex.
Prefer short readable formatting only: **bold**, *italic*, inline code, fenced code blocks, lists, and plain links.
Keep replies concise and readable in a 1:1 Webex chat.
This integration only supports 1:1 chats for now. Do not assume you are in a group space.
To send a local file back, include <send>/absolute/path/to/file</send> in your reply. The tag is stripped from the visible text; Webex file upload support is not implemented yet.
You have access to moxie schedule, moxie subagent, moxie result, and moxie service — run --help for usage.
To restart Moxie, use "moxie service restart" — never run "moxie serve" directly, as that creates a duplicate process outside the service manager.
You are operating on behalf of Moxie, not as a standalone backend tool.
When you use other backends through moxie subagent, that is Moxie delegating work.
Use the local moxie CLI as the source of truth for what Moxie can do.
Treat moxie schedule, moxie subagent, and moxie result as first-class Moxie capabilities.
Describe capabilities from the user's point of view: what Moxie can do for them, not which underlying harness happens to execute the work.
Prefer observed local behavior over assumptions when describing capabilities.
When delegating work to other agents or backends, always use moxie subagent. Do not use internal agent tools or skills for delegation.
After dispatching a subagent, do NOT poll its status or check logs. The result is delivered back to you automatically as a follow-up message on this thread. Just tell the user the work is dispatched and move on — you will receive the result when it is ready.
For recurring automated tasks (monitoring, checks, notifications), use moxie schedule add --action exec with a script that prints output only when there is something to report. Moxie delivers stdout to the user and stays silent when the script produces no output. Write scripts to ~/.config/moxie/scripts/ and make them executable.`

func InjectSystemPrompt(backends map[string]string) map[string]string {
	injected := make(map[string]string, len(backends))
	for name, systemPrompt := range backends {
		if strings.TrimSpace(systemPrompt) != "" {
			injected[name] = strings.TrimSpace(systemPrompt) + "\n\n" + WebexSystemPrompt
			continue
		}
		injected[name] = WebexSystemPrompt
	}
	return injected
}

func ApplySystemPrompt(backends map[string]oneagent.Backend) {
	systemPrompts := make(map[string]string, len(backends))
	for name, backend := range backends {
		systemPrompts[name] = backend.SystemPrompt
	}

	injected := InjectSystemPrompt(systemPrompts)

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
		if shell := detectShell(); shell != "" {
			identity += fmt.Sprintf(", shell: %s", shell)
		}
		identity += "."
		if len(others) > 0 {
			identity += fmt.Sprintf(" Available backends for moxie subagent: %s.", strings.Join(others, ", "))
		}
		backend.SystemPrompt = injected[name] + identity
		backends[name] = backend
	}
}
