package slack

import (
	"github.com/1broseidon/moxie/internal/prompt"
	"github.com/1broseidon/oneagent"
)

const SlackSystemPrompt = `## Output Format
You are responding via a Slack bot. Format replies using Slack-compatible markdown (mrkdwn).
Do not use HTML. Avoid markdown tables and overly complex formatting that may render poorly in Slack.
Prefer short readable formatting only: *bold*, _italic_, ~strikethrough~, inline code, fenced code blocks, lists, and plain links.
Keep replies concise and readable in Slack.

## File Delivery
To send a local file back, include <send>/absolute/path/to/file</send> in your reply. The tag is stripped from the visible text and the file is uploaded separately to the channel.`

func InjectSystemPrompt(backends map[string]string) map[string]string {
	return prompt.InjectTransportPrompt(backends, SlackSystemPrompt)
}

func ApplySystemPrompt(backends map[string]oneagent.Backend) {
	prompt.ApplySystemPrompts(backends, SlackSystemPrompt)
}
