package chat

import (
	"strings"
	"testing"

	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

func useTempConfigDir(t *testing.T) {
	t.Helper()
	restore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restore)
}

func TestHandleInboundBuildsJob(t *testing.T) {
	useTempConfigDir(t)

	st := store.State{Backend: "claude", ThreadID: "chat-1"}
	msg := InboundMessage{
		EventID:      "123",
		Source:       "telegram",
		Conversation: ConversationRef{Provider: ProviderTelegram, ChannelID: "412407481"},
		Text:         "hello",
	}

	settings := Settings{Workspaces: map[string]string{}}
	got := HandleInbound(msg, settings, "/tmp/default", st, &oneagent.Client{})
	if got.ImmediateReply != "" {
		t.Fatalf("ImmediateReply = %q, want empty", got.ImmediateReply)
	}
	if got.Job == nil {
		t.Fatal("expected job to be created")
	}
	if got.Job.Source != "telegram" || got.Job.SourceEventID != "123" {
		t.Fatalf("job source fields = %+v", got.Job)
	}
	conv := ParseConversationID(got.Job.ConversationID)
	if conv.Provider != ProviderTelegram || conv.ChannelID != "412407481" {
		t.Fatalf("job conversation = %+v", conv)
	}
	if got.Job.CWD != "/tmp/default" {
		t.Fatalf("job CWD = %q, want /tmp/default", got.Job.CWD)
	}
}

func TestHandleInboundBuildsImmediateCommandReply(t *testing.T) {
	useTempConfigDir(t)

	client := &oneagent.Client{Backends: map[string]oneagent.Backend{"claude": {DefaultModel: "sonnet"}}}
	got := HandleInbound(
		InboundMessage{
			Source:       "telegram",
			Conversation: ConversationRef{Provider: ProviderTelegram, ChannelID: "412407481"},
			Text:         "/model",
		},
		Settings{Workspaces: map[string]string{}},
		"",
		store.State{Backend: "claude"},
		client,
	)
	if !strings.Contains(got.ImmediateReply, "Backend: claude") {
		t.Fatalf("ImmediateReply = %q", got.ImmediateReply)
	}
	if got.Job != nil {
		t.Fatal("expected no job for command")
	}
}
