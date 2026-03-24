package webex

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/store"
)

type jobState struct {
	ReplyConversation chat.ConversationRef `json:"reply_conversation"`
	StatusMessage     chat.MessageRef      `json:"status_message"`
}

func statusDir() string {
	return filepath.Join(store.ConfigDir(), "webex-status")
}

func statusFile(jobID string) string {
	return filepath.Join(statusDir(), jobID+".json")
}

func readJobState(jobID string) jobState {
	data, err := os.ReadFile(statusFile(jobID))
	if err != nil {
		return jobState{}
	}
	var st jobState
	if err := json.Unmarshal(data, &st); err != nil {
		return jobState{}
	}
	return st
}

func writeJobState(jobID string, st jobState) {
	if err := os.MkdirAll(statusDir(), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = os.WriteFile(statusFile(jobID), data, 0o600)
}

func removeJobState(jobID string) {
	_ = os.Remove(statusFile(jobID))
}
