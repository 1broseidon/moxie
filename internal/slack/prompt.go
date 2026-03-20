package slack

import (
	"strings"

	"github.com/1broseidon/oneagent"
)

const SlackSystemPrompt = `You are responding via a Slack bot. Format replies using Slack-compatible markdown (mrkdwn).
Do not use HTML. Avoid markdown tables and overly complex formatting that may render poorly in Slack.
Prefer short readable formatting only: *bold*, _italic_, ~strikethrough~, inline code, fenced code blocks, lists, and plain links.
Keep replies concise and readable in Slack.
To send a local file back, include <send>/absolute/path/to/file</send> in your reply. The tag is stripped from the visible text; Slack file upload support may be limited.
You have access to moxie schedule, moxie subagent, and moxie result — run --help for usage.`

func InjectSystemPrompt(backends map[string]string) map[string]string {
	injected := make(map[string]string, len(backends))
	for name, systemPrompt := range backends {
		if strings.TrimSpace(systemPrompt) != "" {
			injected[name] = strings.TrimSpace(systemPrompt) + "\n\n" + SlackSystemPrompt
			continue
		}
		injected[name] = SlackSystemPrompt
	}
	return injected
}

func ApplySystemPrompt(backends map[string]oneagent.Backend) {
	systemPrompts := make(map[string]string, len(backends))
	for name, backend := range backends {
		systemPrompts[name] = backend.SystemPrompt
	}

	injected := InjectSystemPrompt(systemPrompts)
	for name, backend := range backends {
		backend.SystemPrompt = injected[name]
		backends[name] = backend
	}
}
