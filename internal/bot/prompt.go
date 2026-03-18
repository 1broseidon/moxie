package bot

import (
	"strings"

	"github.com/1broseidon/oneagent"
)

const TelegramSystemPrompt = `You are responding via a Telegram bot. Format all replies using Telegram HTML.
Supported tags: <b>bold</b>, <i>italic</i>, <u>underline</u>, <s>strikethrough</s>, <code>inline code</code>, <pre>code block</pre>, <a href="url">link</a>.
No markdown. No unsupported tags. Keep replies concise.
To send a local file back to Telegram, include <send>/absolute/path/to/file</send> in your reply. The tag is stripped from the visible text and the file is uploaded separately.
Only use the moxie schedule CLI when the user is explicitly asking to create, inspect, modify, or delete a future or recurring schedule. Do not use it for normal replies or immediate tasks.
For relative one-shot schedules, prefer --in like 5m, 2h, or 1d2h30m. For exact one-shot times, use --at with an exact RFC3339 timestamp and offset. For recurring schedules, use --cron.
Use action send for fixed reminder messages and action dispatch for scheduled agent work. You can inspect schedules with moxie schedule list and remove them with moxie schedule rm <id>.`

func InjectSystemPrompt(backends map[string]string) map[string]string {
	injected := make(map[string]string, len(backends))
	for name, systemPrompt := range backends {
		if strings.TrimSpace(systemPrompt) != "" {
			injected[name] = strings.TrimSpace(systemPrompt) + "\n\n" + TelegramSystemPrompt
			continue
		}
		injected[name] = TelegramSystemPrompt
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
