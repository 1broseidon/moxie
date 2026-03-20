package slack

import (
	"context"
	"testing"

	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

func useSlackStoreDir(t *testing.T) {
	t.Helper()
	restore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restore)
}

func TestStripLeadingBotMention(t *testing.T) {
	got := stripLeadingBotMention(" <@U123>   /new claude ", "U123")
	if got != "/new claude" {
		t.Fatalf("stripLeadingBotMention() = %q, want /new claude", got)
	}

	got = stripLeadingBotMention("<@U123> <@U123> hello", "U123")
	if got != "hello" {
		t.Fatalf("stripLeadingBotMention(repeated) = %q, want hello", got)
	}
}

func TestInboundFromAppMentionEventTopLevelRepliesInThread(t *testing.T) {
	envelope, ok := inboundFromAppMentionEvent("Ev1", &slackevents.AppMentionEvent{
		User:      "U999",
		Text:      "<@U123> hello there",
		TimeStamp: "1710000000.100",
		Channel:   "C123",
	}, "U123")
	if !ok {
		t.Fatal("expected app mention envelope")
	}
	if envelope.inbound.Text != "hello there" {
		t.Fatalf("inbound text = %q, want hello there", envelope.inbound.Text)
	}
	if envelope.inbound.Conversation != (chat.ConversationRef{Provider: chat.ProviderSlack, ChannelID: "C123"}) {
		t.Fatalf("conversation = %+v", envelope.inbound.Conversation)
	}
	if envelope.reply.ThreadID != "1710000000.100" {
		t.Fatalf("reply thread = %q, want top-level ts", envelope.reply.ThreadID)
	}
}

func TestInboundFromDirectMessageEventKeepsThreadIfPresent(t *testing.T) {
	envelope, ok := inboundFromMessageEvent("Ev2", &slackevents.MessageEvent{
		User:            "U999",
		Text:            "hello",
		Channel:         "D123",
		ChannelType:     "im",
		ThreadTimeStamp: "1710000000.200",
	})
	if !ok {
		t.Fatal("expected dm envelope")
	}
	if envelope.reply.ThreadID != "1710000000.200" {
		t.Fatalf("reply thread = %q, want existing thread", envelope.reply.ThreadID)
	}
}

func TestIsDirectMessageEventRejectsBotAndSubtypes(t *testing.T) {
	if isDirectMessageEvent(&slackevents.MessageEvent{User: "U1", Text: "x", ChannelType: "im", BotID: "B1"}, "U123") {
		t.Fatal("expected bot-authored DM to be ignored")
	}
	if isDirectMessageEvent(&slackevents.MessageEvent{User: "U1", Text: "x", ChannelType: "im", SubType: "message_changed"}, "U123") {
		t.Fatal("expected subtype DM to be ignored")
	}
}

func TestChatSettingsPersistsWorkspaces(t *testing.T) {
	useSlackStoreDir(t)

	cfg := &store.Config{Channels: map[string]store.ChannelConfig{}, Workspaces: map[string]string{}}
	settings := chatSettings(cfg)
	settings.SaveWorkspaces(map[string]string{"repo": "/tmp/repo"})

	saved, err := store.LoadConfig()
	if err == nil {
		t.Fatalf("expected invalid config because no valid channel, got %+v", saved)
	}
	if cfg.Workspaces["repo"] != "/tmp/repo" {
		t.Fatalf("workspace not updated in config: %+v", cfg.Workspaces)
	}
}

func TestAdapterRunWithoutSocketFails(t *testing.T) {
	adapter := &Adapter{}
	if err := adapter.Run(context.Background()); err == nil {
		t.Fatal("expected missing socket error")
	}
}

func TestNormalizeSlashCommandText(t *testing.T) {
	if got := normalizeSlashCommandText("model"); got != "/model" {
		t.Fatalf("normalizeSlashCommandText(model) = %q", got)
	}
	if got := normalizeSlashCommandText("/cwd tele"); got != "/cwd tele" {
		t.Fatalf("normalizeSlashCommandText(/cwd tele) = %q", got)
	}
	if got := normalizeSlashCommandText("   "); got != "" {
		t.Fatalf("normalizeSlashCommandText(empty) = %q", got)
	}
}

func TestHandleSlashCommandRespondsEphemerally(t *testing.T) {
	useSlackStoreDir(t)

	cfg := &store.Config{
		Channels: map[string]store.ChannelConfig{
			"slack": {
				Provider: "slack",
				Token:    "xoxb-test",
				AppToken: "xapp-test",
			},
		},
		Workspaces: map[string]string{},
	}
	client := &Adapter{
		cfg: cfg,
		client: &oneagent.Client{
			Backends: map[string]oneagent.Backend{
				"claude": {DefaultModel: "sonnet"},
			},
		},
	}

	payload := client.slashCommandPayload(slack.SlashCommand{
		Text:      "model",
		ChannelID: "C123",
		UserID:    "U123",
		TriggerID: "1337.42",
	})

	if payload["response_type"] != slack.ResponseTypeEphemeral {
		t.Fatalf("response type = %v, want ephemeral", payload["response_type"])
	}
	text, _ := payload["text"].(string)
	if text == "" {
		t.Fatal("expected slash command response text")
	}
}
