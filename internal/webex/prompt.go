package webex

import (
	"github.com/1broseidon/moxie/internal/prompt"
	"github.com/1broseidon/oneagent"
)

const WebexSystemPrompt = `## Output Format
You are responding via a Webex direct-message bot. Format replies using simple Webex-friendly markdown.
Do not use HTML. Avoid markdown tables and overly complex formatting that may render poorly in Webex.
Prefer short readable formatting only: **bold**, *italic*, inline code, fenced code blocks, lists, and plain links.
Keep replies concise and readable in a 1:1 Webex chat.
This integration only supports 1:1 chats for now. Do not assume you are in a group space.

## File Delivery
To send a local file back, include <send>/absolute/path/to/file</send> in your reply. The tag is stripped from the visible text and the file is uploaded separately. Webex supports one file per message.`

func InjectSystemPrompt(backends map[string]string) map[string]string {
	return prompt.InjectTransportPrompt(backends, WebexSystemPrompt)
}

func ApplySystemPrompt(backends map[string]oneagent.Backend) {
	prompt.ApplySystemPrompts(backends, WebexSystemPrompt)
}
