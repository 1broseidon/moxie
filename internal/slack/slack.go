package slack

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/scheduler"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
	goslack "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

type socketClient struct {
	*socketmode.Client
}

func (c *socketClient) Events() <-chan socketmode.Event {
	return c.Client.Events
}

type socketRunner interface {
	RunContext(ctx context.Context) error
	Ack(req socketmode.Request, payload ...interface{})
	Events() <-chan socketmode.Event
}

type authTester interface {
	messenger
	AuthTest() (*goslack.AuthTestResponse, error)
}

type Adapter struct {
	cfg        *store.Config
	defaultCWD string
	client     *oneagent.Client
	schedules  *scheduler.Store
	api        authTester
	socket     socketRunner
	botUserID  string
}

func (a *Adapter) API() messenger {
	return a.api
}

func New(cfg *store.Config, defaultCWD string, client *oneagent.Client, schedules *scheduler.Store) (*Adapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("slack adapter requires config")
	}
	channel, err := cfg.Slack()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(channel.Token) == "" {
		return nil, fmt.Errorf("config missing slack bot token")
	}

	api := goslack.New(channel.Token, goslack.OptionAppLevelToken(channel.AppToken))
	if _, err := api.AuthTest(); err != nil {
		return nil, fmt.Errorf("slack auth test failed: %w", err)
	}

	return NewWithClients(cfg, defaultCWD, client, schedules, api, &socketClient{Client: socketmode.New(api)}), nil
}

func NewWithClients(cfg *store.Config, defaultCWD string, client *oneagent.Client, schedules *scheduler.Store, api authTester, socket socketRunner) *Adapter {
	botUserID := ""
	if api != nil {
		if auth, err := api.AuthTest(); err == nil && auth != nil {
			botUserID = auth.UserID
		}
	}
	return &Adapter{
		cfg:        cfg,
		defaultCWD: defaultCWD,
		client:     client,
		schedules:  schedules,
		api:        api,
		socket:     socket,
		botUserID:  botUserID,
	}
}

func chatSettings(cfg *store.Config) chat.Settings {
	workspaces := cfg.Workspaces
	if workspaces == nil {
		workspaces = map[string]string{}
	}
	return chat.Settings{
		Workspaces: workspaces,
		SaveWorkspaces: func(updated map[string]string) {
			cfg.Workspaces = updated
			store.SaveConfig(*cfg)
		},
	}
}

func (a *Adapter) Run(ctx context.Context) error {
	if a.socket == nil {
		return fmt.Errorf("slack adapter requires socket client")
	}
	go a.eventLoop(ctx)
	return a.socket.RunContext(ctx)
}

func (a *Adapter) eventLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-a.socket.Events():
			if !ok {
				return
			}
			a.handleSocketEvent(evt)
		}
	}
}

func (a *Adapter) handleSocketEvent(evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeConnecting:
		log.Printf("slack socket connecting")
	case socketmode.EventTypeConnected:
		log.Printf("slack socket connected")
	case socketmode.EventTypeConnectionError:
		log.Printf("slack socket connection error: %+v", evt.Data)
	case socketmode.EventTypeInvalidAuth:
		log.Printf("slack socket invalid auth")
	case socketmode.EventTypeEventsAPI:
		if evt.Request != nil {
			a.socket.Ack(*evt.Request)
		}
		eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			log.Printf("ignored unexpected slack event payload: %T", evt.Data)
			return
		}
		go a.handleEventsAPIEvent(eventsAPIEvent)
	case socketmode.EventTypeSlashCommand:
		cmd, ok := evt.Data.(goslack.SlashCommand)
		if !ok {
			log.Printf("ignored unexpected slash command payload: %T", evt.Data)
			return
		}
		if evt.Request != nil {
			a.socket.Ack(*evt.Request, a.slashCommandPayload(cmd))
			return
		}
		go a.handleSlashCommand(cmd)
	}
}

func (a *Adapter) handleEventsAPIEvent(evt slackevents.EventsAPIEvent) {
	if evt.Type != slackevents.CallbackEvent {
		return
	}
	eventID := ""
	if callback, ok := evt.Data.(*slackevents.EventsAPICallbackEvent); ok && callback != nil {
		eventID = callback.EventID
	}
	switch inner := evt.InnerEvent.Data.(type) {
	case *slackevents.MessageEvent:
		a.handleMessageEvent(eventID, inner)
	case *slackevents.AppMentionEvent:
		a.handleAppMentionEvent(eventID, inner)
	}
}

type inboundEnvelope struct {
	inbound chat.InboundMessage
	reply   chat.ConversationRef
}

func senderName(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "unknown"
	}
	return "<@" + userID + ">"
}

func slackConversation(channelID, threadTS string) chat.ConversationRef {
	return chat.ConversationRef{
		Provider:  chat.ProviderSlack,
		ChannelID: channelID,
		ThreadID:  strings.TrimSpace(threadTS),
	}
}

func stripLeadingBotMention(text, botUserID string) string {
	text = strings.TrimSpace(text)
	if text == "" || botUserID == "" {
		return text
	}
	mention := "<@" + botUserID + ">"
	for {
		trimmed := strings.TrimSpace(text)
		if !strings.HasPrefix(trimmed, mention) {
			return trimmed
		}
		text = strings.TrimSpace(strings.TrimPrefix(trimmed, mention))
	}
}

func isDirectMessageEvent(evt *slackevents.MessageEvent, botUserID string) bool {
	if evt == nil {
		return false
	}
	if evt.ChannelType != "im" || evt.SubType != "" || strings.TrimSpace(evt.BotID) != "" {
		return false
	}
	if evt.User == "" || evt.User == botUserID {
		return false
	}
	return strings.TrimSpace(evt.Text) != ""
}

func inboundFromMessageEvent(eventID string, evt *slackevents.MessageEvent) (inboundEnvelope, bool) {
	if evt == nil {
		return inboundEnvelope{}, false
	}
	conversation := slackConversation(evt.Channel, evt.ThreadTimeStamp)
	return inboundEnvelope{
		inbound: chat.InboundMessage{
			EventID:      strings.TrimSpace(eventID),
			Source:       string(chat.ProviderSlack),
			Conversation: conversation,
			SenderName:   senderName(evt.User),
			Text:         evt.Text,
			Prompt:       evt.Text,
		},
		reply: conversation,
	}, true
}

func isAppMentionEvent(evt *slackevents.AppMentionEvent, botUserID string) bool {
	if evt == nil || strings.TrimSpace(evt.Text) == "" || strings.TrimSpace(evt.BotID) != "" {
		return false
	}
	return evt.User != "" && evt.User != botUserID
}

func inboundFromAppMentionEvent(eventID string, evt *slackevents.AppMentionEvent, botUserID string) (inboundEnvelope, bool) {
	if evt == nil {
		return inboundEnvelope{}, false
	}
	text := stripLeadingBotMention(evt.Text, botUserID)
	if text == "" {
		return inboundEnvelope{}, false
	}
	conversation := slackConversation(evt.Channel, evt.ThreadTimeStamp)
	reply := conversation
	if reply.ThreadID == "" {
		reply.ThreadID = strings.TrimSpace(evt.TimeStamp)
	}
	return inboundEnvelope{
		inbound: chat.InboundMessage{
			EventID:      strings.TrimSpace(eventID),
			Source:       string(chat.ProviderSlack),
			Conversation: conversation,
			SenderName:   senderName(evt.User),
			Text:         text,
			Prompt:       text,
		},
		reply: reply,
	}, true
}

func (a *Adapter) handleMessageEvent(eventID string, evt *slackevents.MessageEvent) {
	if !isDirectMessageEvent(evt, a.botUserID) {
		return
	}
	envelope, ok := inboundFromMessageEvent(eventID, evt)
	if !ok {
		return
	}
	a.processInbound(envelope)
}

func (a *Adapter) handleAppMentionEvent(eventID string, evt *slackevents.AppMentionEvent) {
	if !isAppMentionEvent(evt, a.botUserID) {
		return
	}
	envelope, ok := inboundFromAppMentionEvent(eventID, evt, a.botUserID)
	if !ok {
		return
	}
	a.processInbound(envelope)
}

func (a *Adapter) processInbound(envelope inboundEnvelope) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic in slack inbound handler: %v", r)
			SendImmediate(a.api, envelope.reply, "Internal error - bot recovered.")
		}
	}()

	st := store.ReadConversationState(envelope.inbound.Conversation.ID())
	result := chat.HandleInbound(envelope.inbound, chatSettings(a.cfg), a.defaultCWD, st, a.client)
	if result.ImmediateReply != "" {
		SendImmediate(a.api, envelope.reply, result.ImmediateReply)
	}
	if result.Job == nil {
		return
	}
	result.Job.ReplyConversation = envelope.reply.ID()
	writeJobState(result.Job.ID, jobState{ReplyConversation: envelope.reply})
	ProcessJob(*result.Job, a.api, a.client, a.schedules)
}

func normalizeSlashCommandText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "/") {
		return raw
	}
	return "/" + raw
}

func (a *Adapter) slashCommandPayload(cmd goslack.SlashCommand) map[string]interface{} {
	text := normalizeSlashCommandText(cmd.Text)
	switch {
	case text == "":
		return map[string]interface{}{
			"text":          "Try `/moxie model`, `/moxie new`, `/moxie cwd`, `/moxie threads`, or `/moxie compact`.",
			"response_type": goslack.ResponseTypeEphemeral,
		}
	case a.client == nil:
		return map[string]interface{}{
			"text":          "Slack command handler is not ready.",
			"response_type": goslack.ResponseTypeEphemeral,
		}
	}

	result := chat.HandleInbound(chat.InboundMessage{
		EventID: cmd.TriggerID,
		Source:  string(chat.ProviderSlack),
		Conversation: chat.ConversationRef{
			Provider:  chat.ProviderSlack,
			ChannelID: cmd.ChannelID,
		},
		SenderName: senderName(cmd.UserID),
		Text:       text,
		Prompt:     text,
	}, chatSettings(a.cfg), a.defaultCWD, store.ReadConversationState(chat.ConversationRef{
		Provider:  chat.ProviderSlack,
		ChannelID: cmd.ChannelID,
	}.ID()), a.client)

	switch {
	case result.ImmediateReply != "":
		return map[string]interface{}{
			"text":          result.ImmediateReply,
			"response_type": goslack.ResponseTypeEphemeral,
		}
	case result.Job != nil:
		return map[string]interface{}{
			"text":          "Slash commands currently support Moxie control commands only. Use a DM or app mention for prompts.",
			"response_type": goslack.ResponseTypeEphemeral,
		}
	default:
		return map[string]interface{}{
			"text":          "No action taken.",
			"response_type": goslack.ResponseTypeEphemeral,
		}
	}
}

func (a *Adapter) handleSlashCommand(cmd goslack.SlashCommand) {
	payload := a.slashCommandPayload(cmd)
	text, _ := payload["text"].(string)
	responseType, _ := payload["response_type"].(string)
	_ = SendWebhookResponse(cmd.ResponseURL, text, responseType)
}
