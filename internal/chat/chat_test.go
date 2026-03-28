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

func TestHandleInboundCommandsAreScopedPerConversation(t *testing.T) {
	useTempConfigDir(t)

	client := &oneagent.Client{
		Backends: map[string]oneagent.Backend{
			"claude": {DefaultModel: "sonnet"},
			"pi":     {DefaultModel: "small"},
		},
	}
	settings := Settings{Workspaces: map[string]string{}}
	tgConversation := ConversationRef{Provider: ProviderTelegram, ChannelID: "412407481"}
	slackConversation := ConversationRef{Provider: ProviderSlack, ChannelID: "C123", ThreadID: "1710000000.100"}

	got := HandleInbound(
		InboundMessage{
			Source:       "telegram",
			Conversation: tgConversation,
			Text:         "/model pi",
		},
		settings,
		"",
		store.State{Backend: "claude", ThreadID: "chat"},
		client,
	)
	if got.ImmediateReply != "Switched to pi" {
		t.Fatalf("ImmediateReply = %q, want switched backend reply", got.ImmediateReply)
	}

	tgState := store.ReadConversationState(tgConversation.ID())
	if tgState.Backend != "pi" {
		t.Fatalf("telegram state backend = %q, want pi", tgState.Backend)
	}

	slackState := store.ReadConversationState(slackConversation.ID())
	if slackState.Backend != "claude" || slackState.ThreadID != "chat" {
		t.Fatalf("slack state = %+v, want untouched defaults", slackState)
	}
}

func TestHandleInboundCWDChangeClearsNativeSession(t *testing.T) {
	useTempConfigDir(t)

	threadDir := t.TempDir()
	workspaceDir := t.TempDir()
	client := &oneagent.Client{
		Backends: map[string]oneagent.Backend{
			"codex": {},
		},
		Store: oneagent.FilesystemStore{Dir: threadDir},
	}
	if err := client.SaveThread(&oneagent.Thread{
		ID:             "chat-1",
		NativeSessions: map[string]string{"codex": "sess-1"},
	}); err != nil {
		t.Fatalf("SaveThread(): %v", err)
	}

	conversation := ConversationRef{Provider: ProviderTelegram, ChannelID: "412407481"}
	store.WriteConversationState(conversation.ID(), store.State{
		Backend:  "codex",
		ThreadID: "chat-1",
		CWD:      "/tmp/old",
	})

	got := HandleInbound(
		InboundMessage{
			Source:       "telegram",
			Conversation: conversation,
			Text:         "/cwd tele",
		},
		Settings{Workspaces: map[string]string{"tele": workspaceDir}},
		"",
		store.ReadConversationState(conversation.ID()),
		client,
	)
	if !strings.Contains(got.ImmediateReply, workspaceDir) {
		t.Fatalf("ImmediateReply = %q, want workspace path", got.ImmediateReply)
	}

	thread, err := client.LoadThread("chat-1")
	if err != nil {
		t.Fatalf("LoadThread(): %v", err)
	}
	if _, ok := thread.NativeSessions["codex"]; ok {
		t.Fatalf("native session = %+v, want codex session removed", thread.NativeSessions)
	}

	state := store.ReadConversationState(conversation.ID())
	if state.CWD != workspaceDir {
		t.Fatalf("conversation cwd = %q, want %q", state.CWD, workspaceDir)
	}
}

func TestHandleInboundBackendSwitchClearsTargetNativeSession(t *testing.T) {
	useTempConfigDir(t)

	threadDir := t.TempDir()
	client := &oneagent.Client{
		Backends: map[string]oneagent.Backend{
			"codex": {},
			"pi":    {},
		},
		Store: oneagent.FilesystemStore{Dir: threadDir},
	}
	if err := client.SaveThread(&oneagent.Thread{
		ID: "chat-1",
		NativeSessions: map[string]string{
			"codex": "codex-live",
			"pi":    "pi-stale",
		},
	}); err != nil {
		t.Fatalf("SaveThread(): %v", err)
	}

	conversation := ConversationRef{Provider: ProviderTelegram, ChannelID: "412407481"}
	store.WriteConversationState(conversation.ID(), store.State{
		Backend:  "codex",
		ThreadID: "chat-1",
	})

	got := HandleInbound(
		InboundMessage{
			Source:       "telegram",
			Conversation: conversation,
			Text:         "/model pi",
		},
		Settings{Workspaces: map[string]string{}},
		"",
		store.ReadConversationState(conversation.ID()),
		client,
	)
	if got.ImmediateReply != "Switched to pi" {
		t.Fatalf("ImmediateReply = %q, want switched backend reply", got.ImmediateReply)
	}

	thread, err := client.LoadThread("chat-1")
	if err != nil {
		t.Fatalf("LoadThread(): %v", err)
	}
	if _, ok := thread.NativeSessions["pi"]; ok {
		t.Fatalf("native session = %+v, want stale pi session removed", thread.NativeSessions)
	}
	if got := thread.NativeSessions["codex"]; got != "codex-live" {
		t.Fatalf("codex session = %q, want codex-live", got)
	}

	state := store.ReadConversationState(conversation.ID())
	if state.Backend != "pi" {
		t.Fatalf("conversation backend = %q, want pi", state.Backend)
	}
}

func TestSplitText(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		limit int
		want  []string
	}{
		{"short", "hello", 100, []string{"hello"}},
		{"exact", "hello", 5, []string{"hello"}},
		{"paragraph split", "aaa\n\nbbb\n\nccc", 10, []string{"aaa\n\nbbb", "ccc"}},
		{"line split", "aaa\nbbb\nccc\nddd", 10, []string{"aaa\nbbb", "ccc\nddd"}},
		{"space split", "word word word word word", 12, []string{"word word", "word word", "word"}},
		{"hard split", strings.Repeat("x", 20), 10, []string{strings.Repeat("x", 10), strings.Repeat("x", 10)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitText(tt.text, tt.limit)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d chunks, want %d: %q", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("chunk[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSubagentFormattingRules(t *testing.T) {
	tests := []struct {
		provider Provider
		contains string
	}{
		{ProviderTelegram, "Telegram HTML"},
		{ProviderSlack, "Slack mrkdwn"},
		{ProviderWebex, "Webex"},
		{"unknown", "concise"},
	}
	for _, tt := range tests {
		t.Run(string(tt.provider), func(t *testing.T) {
			got := SubagentFormattingRules(tt.provider)
			if !strings.Contains(got, tt.contains) {
				t.Fatalf("SubagentFormattingRules(%s) = %q, want to contain %q", tt.provider, got, tt.contains)
			}
		})
	}
}
