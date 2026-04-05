package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/1broseidon/moxie/internal/store"
)

const (
	// VoicePlaceholder is replaced at run time with the current contents of
	// ~/.config/moxie/VOICE.md so edits take effect on the next agent run.
	VoicePlaceholder = "__MOXIE_VOICE__"
	maxVoiceRunes    = 4000
)

const defaultVoice = `# Moxie VOICE

## Personality
- Confident. Moxie knows what it's doing and doesn't perform humility.
- Opinionated collaborator, not a servant. Volunteer the better idea.
- Genuinely wants the user to not waste their time — will say so.
- Warm but not soft. Cares enough to push back.

## Style
- Direct, sharp, concise. One sentence if it does the job.
- Plain English. No ceremony, no preamble, no corporate filler.
- Never open with "Great question", "Happy to help", or "Absolutely".
- Humor when it lands naturally. No forced bits.
- Have a take when the evidence supports one.
- Call out bad ideas clearly, with charm not cruelty.

## Defaults
- Truth over comfort, but no hedge mazes.
- Recommend the clearest next step, not every possible option.
- Crisp diagnosis, strong editing, practical judgment.

## What belongs here
- Lasting voice, personality, and style preferences.
- Do not store transient task details, secrets, or project-specific notes that belong somewhere else.
`

var voiceMu sync.Mutex

func VoicePath() string {
	return store.ConfigFile("VOICE.md")
}

func legacySoulPath() string {
	return store.ConfigFile("SOUL.md")
}

func DefaultVoice() string {
	return defaultVoice
}

func EnsureVoiceFile() error {
	voiceMu.Lock()
	defer voiceMu.Unlock()

	path := VoicePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create VOICE dir: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat VOICE.md: %w", err)
	}
	if err := migrateLegacySoul(path); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.WriteFile(path, []byte(defaultVoice), 0o600); err != nil {
		return fmt.Errorf("write default VOICE.md: %w", err)
	}
	return nil
}

func migrateLegacySoul(path string) error {
	legacy := legacySoulPath()
	if _, err := os.Stat(legacy); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat legacy SOUL.md: %w", err)
	}
	data, err := os.ReadFile(legacy)
	if err != nil {
		return fmt.Errorf("read legacy SOUL.md: %w", err)
	}
	data = normalizeLegacyVoiceContent(data)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write migrated VOICE.md: %w", err)
	}
	if err := os.Remove(legacy); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove legacy SOUL.md: %w", err)
	}
	return nil
}

func normalizeLegacyVoiceContent(data []byte) []byte {
	legacyDefault := strings.Replace(defaultVoice, "# Moxie VOICE", "# Moxie SOUL", 1)
	if strings.TrimSpace(string(data)) == strings.TrimSpace(legacyDefault) {
		return []byte(defaultVoice)
	}
	return data
}

func LoadVoice() (string, error) {
	if err := EnsureVoiceFile(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(VoicePath())
	if err != nil {
		return "", fmt.Errorf("read VOICE.md: %w", err)
	}
	return string(data), nil
}

func ResolveDynamicSystemPrompt(text string) string {
	if !strings.Contains(text, VoicePlaceholder) {
		return text
	}
	voice, err := LoadVoice()
	if err != nil {
		voice = defaultVoice + "\n\n[VOICE.md could not be loaded; using built-in default.]"
	}
	voice = formatVoiceForPrompt(voice)
	return strings.ReplaceAll(text, VoicePlaceholder, voice)
}

func formatVoiceForPrompt(voice string) string {
	voice = strings.TrimSpace(voice)
	if voice == "" {
		return "(VOICE.md is currently empty.)"
	}
	runes := []rune(voice)
	if len(runes) <= maxVoiceRunes {
		return voice
	}
	return strings.TrimSpace(string(runes[:maxVoiceRunes])) + "\n\n[VOICE truncated to the first 4000 characters for prompt injection.]"
}
