package prompt

import (
	"fmt"
)

func FormatMediaPrompt(kind, path, caption, fallbackRequest string) string {
	line := fmt.Sprintf("User sent %s: %s", kind, path)
	if caption != "" {
		return line + "\nCaption: " + caption
	}
	return line + "\nRequest: " + fallbackRequest
}
