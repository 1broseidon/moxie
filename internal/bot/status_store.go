package bot

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/store"
)

type telegramStatusState struct {
	Message chat.MessageRef `json:"message"`
	HTML    string          `json:"html,omitempty"`
}

func statusDir() string {
	return filepath.Join(store.ConfigDir(), "telegram-status")
}

func statusFile(jobID string) string {
	return filepath.Join(statusDir(), jobID+".json")
}

func readStatus(jobID string) telegramStatusState {
	data, err := os.ReadFile(statusFile(jobID))
	if err != nil {
		return telegramStatusState{}
	}
	var st telegramStatusState
	if err := json.Unmarshal(data, &st); err != nil {
		return telegramStatusState{}
	}
	return st
}

func writeStatus(jobID string, st telegramStatusState) {
	if err := os.MkdirAll(statusDir(), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = os.WriteFile(statusFile(jobID), data, 0o600)
}

func removeStatus(jobID string) {
	_ = os.Remove(statusFile(jobID))
}
