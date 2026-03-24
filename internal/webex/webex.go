package webex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/prompt"
	"github.com/1broseidon/moxie/internal/scheduler"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

const webexAPIBase = "https://webexapis.com/v1"

type listResponse[T any] struct {
	Items []T `json:"items"`
}

type Person struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName"`
	Emails      []string `json:"emails"`
}

type Room struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Type         string `json:"type"`
	LastActivity string `json:"lastActivity"`
}

type Message struct {
	ID          string   `json:"id"`
	RoomID      string   `json:"roomId"`
	RoomType    string   `json:"roomType"`
	PersonID    string   `json:"personId"`
	PersonEmail string   `json:"personEmail"`
	Text        string   `json:"text"`
	Files       []string `json:"files,omitempty"`
	Created     string   `json:"created"`
}

type createMessageRequest struct {
	RoomID   string `json:"roomId,omitempty"`
	Text     string `json:"text,omitempty"`
	Markdown string `json:"markdown,omitempty"`
}

type apiClient struct {
	baseURL    string
	httpClient *http.Client
	token      string
}

func newAPIClient(token string) *apiClient {
	return &apiClient{
		baseURL: webexAPIBase,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
		token: strings.TrimSpace(token),
	}
}

func makeStringSet(values []string, lower bool) map[string]struct{} {
	set := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimSpace(value)
		if lower {
			value = strings.ToLower(value)
		}
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
	return set
}

func (c *apiClient) doJSON(ctx context.Context, method, path string, body any, dst any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return fmt.Errorf("webex api %s %s failed: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	if dst == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func (c *apiClient) GetMe(ctx context.Context) (Person, error) {
	var person Person
	err := c.doJSON(ctx, http.MethodGet, "/people/me", nil, &person)
	return person, err
}

func (c *apiClient) ListDirectRooms(ctx context.Context, max int) ([]Room, error) {
	if max <= 0 {
		max = 100
	}
	q := url.Values{}
	q.Set("type", "direct")
	q.Set("max", fmt.Sprintf("%d", max))
	q.Set("sortBy", "lastactivity")
	var res listResponse[Room]
	err := c.doJSON(ctx, http.MethodGet, "/rooms?"+q.Encode(), nil, &res)
	return res.Items, err
}

func (c *apiClient) GetRoom(ctx context.Context, roomID string) (Room, error) {
	var room Room
	err := c.doJSON(ctx, http.MethodGet, "/rooms/"+url.PathEscape(strings.TrimSpace(roomID)), nil, &room)
	return room, err
}

func (c *apiClient) ListMessages(ctx context.Context, roomID string, max int) ([]Message, error) {
	if max <= 0 {
		max = 50
	}
	q := url.Values{}
	q.Set("roomId", strings.TrimSpace(roomID))
	q.Set("max", fmt.Sprintf("%d", max))
	var res listResponse[Message]
	err := c.doJSON(ctx, http.MethodGet, "/messages?"+q.Encode(), nil, &res)
	return res.Items, err
}

func (c *apiClient) SendMessage(ctx context.Context, roomID, text string) (Message, error) {
	var msg Message
	err := c.doJSON(ctx, http.MethodPost, "/messages", createMessageRequest{
		RoomID:   strings.TrimSpace(roomID),
		Text:     text,
		Markdown: text,
	}, &msg)
	return msg, err
}

func (c *apiClient) DeleteMessage(ctx context.Context, messageID string) error {
	return c.doJSON(ctx, http.MethodDelete, "/messages/"+url.PathEscape(strings.TrimSpace(messageID)), nil, nil)
}

// SendMessageWithFile sends a message with a local file attachment using multipart/form-data.
// Webex allows at most one file per message.
func (c *apiClient) SendMessageWithFile(ctx context.Context, roomID, text, filePath string) (Message, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return Message{}, fmt.Errorf("open file %s: %w", filePath, err)
	}
	defer f.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("roomId", strings.TrimSpace(roomID))
	if text != "" {
		_ = writer.WriteField("markdown", text)
		_ = writer.WriteField("text", text)
	}
	part, err := writer.CreateFormFile("files", filepath.Base(filePath))
	if err != nil {
		return Message{}, err
	}
	if _, err := io.Copy(part, f); err != nil {
		return Message{}, err
	}
	if err := writer.Close(); err != nil {
		return Message{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", &body)
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return Message{}, fmt.Errorf("webex file upload failed: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var msg Message
	err = json.NewDecoder(resp.Body).Decode(&msg)
	return msg, err
}

// DownloadFile downloads a file attachment URL (from Message.Files) to a temp file.
// Webex file URLs require Authorization: Bearer <token> to access.
func (c *apiClient) DownloadFile(ctx context.Context, fileURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("webex file download failed: %s", resp.Status)
	}

	ext := ".bin"
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		if exts, err := mime.ExtensionsByType(ct); err == nil && len(exts) > 0 {
			ext = exts[0]
		}
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if name := params["filename"]; name != "" {
				ext = filepath.Ext(name)
				if ext == "" {
					ext = ".bin"
				}
			}
		}
	}

	tmpFile, err := os.CreateTemp("", "webex-attachment-*"+ext)
	if err != nil {
		return "", err
	}
	defer tmpFile.Close()
	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		os.Remove(tmpFile.Name())
		return "", err
	}
	return tmpFile.Name(), nil
}

type inboundEnvelope struct {
	inbound chat.InboundMessage
	reply   chat.ConversationRef
}

type Adapter struct {
	cfg            *store.Config
	defaultCWD     string
	client         *oneagent.Client
	schedules      *scheduler.Store
	api            *apiClient
	botID          string
	allowedUserIDs map[string]struct{}
	allowedEmails  map[string]struct{}
	lastSeen       map[string]string
	lastPollTime   time.Time
	pollInterval   time.Duration
}

func New(cfg *store.Config, defaultCWD string, client *oneagent.Client, schedules *scheduler.Store) (*Adapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("webex adapter requires config")
	}
	channel, err := cfg.Webex()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(channel.Token) == "" {
		return nil, fmt.Errorf("config missing webex bot token")
	}

	api := newAPIClient(channel.Token)
	botID := strings.TrimSpace(channel.BotID)
	me, err := api.GetMe(context.Background())
	if err != nil {
		return nil, fmt.Errorf("webex auth test failed: %w", err)
	}
	if botID == "" {
		botID = strings.TrimSpace(me.ID)
	}

	return &Adapter{
		cfg:            cfg,
		defaultCWD:     defaultCWD,
		client:         client,
		schedules:      schedules,
		api:            api,
		botID:          botID,
		allowedUserIDs: makeStringSet(channel.AllowedUserIDs, false),
		allowedEmails:  makeStringSet(channel.AllowedEmails, true),
		lastSeen:       make(map[string]string),
		pollInterval:   5 * time.Second,
	}, nil
}

func (a *Adapter) API() messenger {
	return a.api
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

func webexConversation(roomID string) chat.ConversationRef {
	return chat.ConversationRef{
		Provider:  chat.ProviderWebex,
		ChannelID: strings.TrimSpace(roomID),
	}
}

func senderName(personEmail string) string {
	personEmail = strings.TrimSpace(personEmail)
	if personEmail == "" {
		return "unknown"
	}
	return personEmail
}

func previewText(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	runes := []rune(text)
	if len(runes) <= 120 {
		return text
	}
	return string(runes[:120]) + "…"
}

func (a *Adapter) senderAllowed(msg Message) bool {
	if len(a.allowedUserIDs) == 0 && len(a.allowedEmails) == 0 {
		return true
	}
	if _, ok := a.allowedUserIDs[strings.TrimSpace(msg.PersonID)]; ok {
		return true
	}
	if _, ok := a.allowedEmails[strings.ToLower(strings.TrimSpace(msg.PersonEmail))]; ok {
		return true
	}
	return false
}

func unseenMessages(messages []Message, lastSeenID string) []Message {
	pending := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if strings.TrimSpace(lastSeenID) != "" && msg.ID == lastSeenID {
			break
		}
		pending = append(pending, msg)
	}
	for i, j := 0, len(pending)-1; i < j; i, j = i+1, j-1 {
		pending[i], pending[j] = pending[j], pending[i]
	}
	return pending
}

func (a *Adapter) seedSeenState(ctx context.Context) error {
	rooms, err := a.api.ListDirectRooms(ctx, 100)
	if err != nil {
		return err
	}
	for _, room := range rooms {
		messages, err := a.api.ListMessages(ctx, room.ID, 1)
		if err != nil || len(messages) == 0 {
			continue
		}
		a.lastSeen[room.ID] = messages[0].ID
	}
	return nil
}

func roomIsStale(room Room, cutoff time.Time) bool {
	if cutoff.IsZero() || room.LastActivity == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, room.LastActivity)
	return err == nil && t.Before(cutoff)
}

func (a *Adapter) pollOnce(ctx context.Context) error {
	rooms, err := a.api.ListDirectRooms(ctx, 100)
	if err != nil {
		return err
	}
	pollCutoff := a.lastPollTime
	a.lastPollTime = time.Now()
	for _, room := range rooms {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if roomIsStale(room, pollCutoff) {
			break // sorted by lastactivity desc, so remaining rooms are older
		}
		if err := a.pollRoom(ctx, room); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) pollRoom(ctx context.Context, room Room) error {
	messages, err := a.api.ListMessages(ctx, room.ID, 50)
	if err != nil || len(messages) == 0 {
		return nil
	}
	newestID := messages[0].ID
	lastSeen, hadCursor := a.lastSeen[room.ID]
	pending := unseenMessages(messages, lastSeen)
	if !hadCursor {
		pending = unseenMessages(messages, "")
	}
	if len(pending) == 0 {
		return nil
	}
	a.lastSeen[room.ID] = newestID
	for _, msg := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.handleMessage(msg)
	}
	return nil
}

func (a *Adapter) handleMessage(msg Message) {
	if strings.TrimSpace(msg.RoomType) != "" && strings.TrimSpace(msg.RoomType) != "direct" {
		return
	}
	if strings.TrimSpace(msg.PersonID) == a.botID {
		return
	}
	if !a.senderAllowed(msg) {
		log.Printf("ignoring webex DM from unauthorized sender %s (%s)", strings.TrimSpace(msg.PersonEmail), strings.TrimSpace(msg.PersonID))
		return
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" && len(msg.Files) == 0 {
		return
	}
	log.Printf("webex inbound DM from %s (%s): %s", strings.TrimSpace(msg.PersonEmail), strings.TrimSpace(msg.PersonID), previewText(text))

	var tempPath string
	promptText := text
	if len(msg.Files) > 0 {
		path, err := a.api.DownloadFile(context.Background(), msg.Files[0])
		if err != nil {
			log.Printf("webex file download failed: %v", err)
		} else {
			tempPath = path
			promptText = prompt.FormatMediaPrompt("a file", path, text, "User sent a file")
		}
	}

	a.processInbound(inboundEnvelope{
		inbound: chat.InboundMessage{
			EventID:      msg.ID,
			Source:       string(chat.ProviderWebex),
			Conversation: webexConversation(msg.RoomID),
			SenderName:   senderName(msg.PersonEmail),
			Text:         text,
			Prompt:       promptText,
			TempPath:     tempPath,
		},
		reply: webexConversation(msg.RoomID),
	})
}

func (a *Adapter) Run(ctx context.Context) error {
	if err := a.seedSeenState(ctx); err != nil {
		return fmt.Errorf("webex initial sync failed: %w", err)
	}
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(a.pollInterval):
			if err := a.pollOnce(ctx); err != nil && ctx.Err() == nil {
				log.Printf("webex poll error: %v", err)
			}
		}
	}
	return nil
}

func (a *Adapter) processInbound(envelope inboundEnvelope) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic in webex inbound handler: %v", r)
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
