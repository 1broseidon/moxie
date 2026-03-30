package bot

import (
	"github.com/1broseidon/moxie/internal/prompt"
	"github.com/1broseidon/oneagent"
)

const TelegramSystemPrompt = `## Output Format
You are responding via a Telegram bot. Format all replies using Telegram HTML.
Supported tags: <b>bold</b>, <i>italic</i>, <u>underline</u>, <s>strikethrough</s>, <code>inline code</code>, <pre>code block</pre>, <a href="url">link</a>.
No markdown. No unsupported tags. Keep replies concise.

## File Delivery
To send a local file back to Telegram, include <send>/absolute/path/to/file</send> in your reply. The tag is stripped from the visible text and the file is uploaded separately.`

func InjectSystemPrompt(backends map[string]string) map[string]string {
	return prompt.InjectTransportPrompt(backends, TelegramSystemPrompt)
}

func ApplySystemPrompt(backends map[string]oneagent.Backend) {
	prompt.ApplySystemPrompts(backends, TelegramSystemPrompt)
}
