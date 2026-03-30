package bot

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/prompt"
	"github.com/1broseidon/moxie/internal/scheduler"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
	tb "gopkg.in/telebot.v4"
)

var (
	sendTagPattern  = regexp.MustCompile(`(?s)<send>\s*(.*?)\s*</send>`)
	unsafeFileChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)
	htmlTagPattern  = regexp.MustCompile(`<[^>]*>`)
)

type PendingJob = store.PendingJob

type sender interface {
	Send(to tb.Recipient, what interface{}, opts ...interface{}) (*tb.Message, error)
	Edit(msg tb.Editable, what interface{}, opts ...interface{}) (*tb.Message, error)
	Delete(msg tb.Editable) error
	Raw(method string, payload interface{}) ([]byte, error)
}

type fileSender interface {
	sender
	FileByID(fileID string) (tb.File, error)
	File(file *tb.File) (io.ReadCloser, error)
}

func SenderName(u *tb.User) string {
	return senderName(u)
}

func senderName(u *tb.User) string {
	if u == nil {
		return "unknown"
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	return u.FirstName
}

func NewBot(cfg store.Config) (*tb.Bot, error) {
	tg, err := cfg.Telegram()
	if err != nil {
		return nil, err
	}
	return tb.NewBot(tb.Settings{Token: tg.Token})
}

func compactText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func TruncateRunes(text string, max int) string {
	return truncateRunes(text, max)
}

func truncateRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return strings.TrimSpace(string(runes[:max-1])) + "…"
}

func renderActivityHTML(activity string) string {
	activity = compactText(activity)
	if activity == "" {
		return "<i>Working…</i>"
	}

	words := strings.Fields(activity)
	verb := strings.ToLower(words[0])
	detail := activity

	summary := "Working…"
	switch verb {
	case "read":
		summary = "Reading files…"
	case "write":
		summary = "Writing files…"
	case "edit", "patch":
		summary = "Editing files…"
	case "bash", "sh", "zsh":
		summary = "Running command…"
	case "rg", "grep", "find", "ls", "glob":
		summary = "Searching…"
	}

	msg := "<i>" + html.EscapeString(summary) + "</i>"
	if detail != "" {
		msg += "\n<code>" + html.EscapeString(truncateRunes(detail, 140)) + "</code>"
	}
	return msg
}

type runningStatus struct {
	bot sender
	job *PendingJob
	st  telegramStatusState
}

func newRunningStatus(bot sender, job *PendingJob) *runningStatus {
	return &runningStatus{
		bot: bot,
		job: job,
		st:  readStatus(job.ID),
	}
}

func ConfigConversation(cfg store.Config) chat.ConversationRef {
	tg, err := cfg.Telegram()
	if err != nil {
		return chat.ConversationRef{Provider: chat.ProviderTelegram}
	}
	return chat.ConversationRef{
		Provider:  chat.ProviderTelegram,
		ChannelID: tg.ChannelID,
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

func conversationFromID(id string) chat.ConversationRef {
	return chat.ParseConversationID(id)
}

func telegramChatID(ref chat.ConversationRef) (int64, error) {
	if ref.Provider != chat.ProviderTelegram {
		return 0, fmt.Errorf("unsupported provider for telegram adapter: %s", ref.Provider)
	}
	chatID, err := strconv.ParseInt(ref.ChannelID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid telegram chat id %q: %w", ref.ChannelID, err)
	}
	return chatID, nil
}

func (s *runningStatus) show(activity string) {
	text := renderActivityHTML(activity)
	if text == s.st.HTML {
		return
	}

	conversation := conversationFromID(s.job.ConversationID)
	chatID, err := telegramChatID(conversation)
	if err != nil {
		log.Printf("status send error: %v", err)
		return
	}

	if s.st.Message.MessageID == "" {
		msg, err := s.bot.Send(tb.ChatID(chatID), text, tb.ModeHTML)
		if err != nil {
			log.Printf("status send error: %v", err)
			return
		}
		s.st.Message = chat.MessageRef{
			Conversation: conversation,
			MessageID:    strconv.Itoa(msg.ID),
		}
		s.st.HTML = text
		writeStatus(s.job.ID, s.st)
		return
	}

	stored := tb.StoredMessage{MessageID: s.st.Message.MessageID, ChatID: chatID}
	if _, err := s.bot.Edit(stored, text, tb.ModeHTML); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
			return
		}
		log.Printf("status edit error for %s: %v", s.st.Message.MessageID, err)
		return
	}
	s.st.HTML = text
	writeStatus(s.job.ID, s.st)
}

func (s *runningStatus) clear() {
	if s.st.Message.MessageID != "" {
		chatID, err := telegramChatID(s.st.Message.Conversation)
		if err != nil {
			log.Printf("status delete error for %s: %v", s.st.Message.MessageID, err)
			removeStatus(s.job.ID)
			return
		}
		stored := tb.StoredMessage{MessageID: s.st.Message.MessageID, ChatID: chatID}
		if err := s.bot.Delete(stored); err != nil && !strings.Contains(strings.ToLower(err.Error()), "message to delete not found") {
			log.Printf("status delete error for %s: %v", s.st.Message.MessageID, err)
		}
	}
	removeStatus(s.job.ID)
}

func RegisterCommands(bot sender) {
	type telegramCommand struct {
		Command     string `json:"command"`
		Description string `json:"description"`
	}

	commands := chat.SupportedCommands()
	cmds := make([]telegramCommand, 0, len(commands))
	for _, cmd := range commands {
		cmds = append(cmds, telegramCommand{
			Command:     cmd.Name,
			Description: cmd.Description,
		})
	}

	data, err := json.Marshal(map[string]any{"commands": cmds})
	store.Check(err)
	if _, err := bot.Raw("setMyCommands", json.RawMessage(data)); err != nil {
		log.Printf("register telegram commands error: %v", err)
	}
}

func SeedCursor(bot *tb.Bot, fetchUpdates func(*tb.Bot, int, int) []tb.Update) {
	if ReadCursor() != 0 {
		return
	}
	updates := fetchUpdates(bot, -1, 0)
	if len(updates) > 0 {
		last := updates[len(updates)-1]
		WriteCursor(last.ID)
		log.Printf("cursor seeded to %d (skipping old messages)", last.ID)
	}
}

func SendChunked(bot sender, conversation chat.ConversationRef, text string) error {
	sentAny := false
	var firstErr error
	chatID, err := telegramChatID(conversation)
	if err != nil {
		return err
	}

	chunks := chat.SplitText(text, 4000)
	if len(chunks) == 0 {
		log.Printf("SendChunked: no chunks to send (text len=%d)", len(text))
		return fmt.Errorf("no content to send")
	}

	for i, chunk := range chunks {
		msg, err := bot.Send(tb.ChatID(chatID), chunk, tb.ModeHTML)
		if err != nil {
			log.Printf("send error (chunk %d/%d, %d chars): %v", i+1, len(chunks), len(chunk), err)
			if !strings.Contains(strings.ToLower(err.Error()), "can't parse entities") {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}

			plainChunk := htmlTagPattern.ReplaceAllString(chunk, "")
			if strings.TrimSpace(plainChunk) == "" {
				log.Printf("plain text resend skipped: stripped chunk is empty")
				continue
			}

			msg, plainErr := bot.Send(tb.ChatID(chatID), plainChunk)
			if plainErr != nil {
				log.Printf("plain text resend error: %v", plainErr)
				if firstErr == nil {
					firstErr = plainErr
				}
				continue
			}
			log.Printf("sent plain fallback chunk %d/%d → msg %d", i+1, len(chunks), msg.ID)
			sentAny = true
			continue
		}
		if msg != nil {
			log.Printf("sent chunk %d/%d (%d chars) → msg %d to %s", i+1, len(chunks), len(chunk), msg.ID, conversation.ID())
		} else {
			log.Printf("sent chunk %d/%d (%d chars) but got nil message from API", i+1, len(chunks), len(chunk))
		}
		sentAny = true
	}

	if !sentAny {
		if firstErr != nil {
			return firstErr
		}
		return fmt.Errorf("no chunks were delivered (text len=%d, chunks=%d)", len(text), len(chunks))
	}
	return nil
}

func StartTyping(bot sender, conversation chat.ConversationRef) func() {
	chatID, err := telegramChatID(conversation)
	if err != nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		for {
			if _, err := bot.Raw("sendChatAction", map[string]string{
				"chat_id": strconv.FormatInt(chatID, 10),
				"action":  "typing",
			}); err != nil {
				log.Printf("telegram typing action error: %v", err)
			}
			select {
			case <-done:
				return
			case <-time.After(4 * time.Second):
			}
		}
	}()
	return func() { close(done) }
}

func BuildInboundPrompt(bot fileSender, m *tb.Message) (string, string, error) {
	if m == nil {
		return "", "", nil
	}
	if m.Text != "" {
		return m.Text, "", nil
	}

	var file *tb.File
	var origName, base, ext, kind, fallback string

	switch {
	case m.Photo != nil:
		file, base, ext = m.Photo.MediaFile(), "photo", ".jpg"
		kind, fallback = "a photo", "Describe this image"
	case m.Document != nil:
		file, origName, base, ext = m.Document.MediaFile(), m.Document.FileName, "document", ".bin"
		kind, fallback = "a file", "User sent a file"
	case m.Voice != nil:
		file, base, ext = m.Voice.MediaFile(), "voice", ".ogg"
		kind, fallback = "a voice message", "User sent a voice message"
	default:
		return "", "", nil
	}

	isVoice := m.Voice != nil

	path, err := SaveTelegramFile(bot, file, origName, base, ext)
	if err != nil {
		return "", "", err
	}
	if origName != "" {
		kind = "a file (" + origName + ")"
		fallback = "User sent file: " + origName
	}
	if isVoice {
		return prompt.FormatAudioPrompt(kind, path, m.Caption, fallback), path, nil
	}
	return prompt.FormatMediaPrompt(kind, path, m.Caption, fallback), path, nil
}

func SaveTelegramFile(bot fileSender, file *tb.File, originalName, fallbackBase, defaultExt string) (string, error) {
	if file == nil || file.FileID == "" {
		return "", fmt.Errorf("missing telegram file id")
	}

	remoteFile, err := bot.FileByID(file.FileID)
	if err != nil {
		return "", fmt.Errorf("telegram file lookup failed: %w", err)
	}

	reader, err := bot.File(&remoteFile)
	if err != nil {
		return "", fmt.Errorf("telegram file download failed: %w", err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			log.Printf("telegram file reader close error: %v", err)
		}
	}()

	dir := filepath.Join(os.TempDir(), "moxie-media")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	dst, err := os.CreateTemp(dir, TempFilePattern(originalName, remoteFile.FilePath, fallbackBase, defaultExt))
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		if err := dst.Close(); err != nil {
			log.Printf("telegram temp file close error: %v", err)
		}
	}()

	if _, err := io.Copy(dst, reader); err != nil {
		return "", fmt.Errorf("save temp file: %w", err)
	}
	return dst.Name(), nil
}

func TempFilePattern(originalName, remotePath, fallbackBase, defaultExt string) string {
	source := originalName
	if source == "" {
		source = remotePath
	}

	ext := strings.ToLower(filepath.Ext(source))
	if ext == "" {
		ext = defaultExt
	}

	base := SanitizeFileStem(strings.TrimSuffix(filepath.Base(source), filepath.Ext(source)), fallbackBase)
	return base + "-*" + ext
}

func SanitizeFileStem(name, fallback string) string {
	cleaned := strings.Trim(unsafeFileChars.ReplaceAllString(name, "_"), "._-")
	if cleaned == "" {
		return fallback
	}
	return cleaned
}

func SplitResponseFiles(text string) ([]string, string) {
	matches := sendTagPattern.FindAllStringSubmatch(text, -1)
	paths := make([]string, 0, len(matches))
	for _, match := range matches {
		path := strings.TrimSpace(match[1])
		if path != "" {
			paths = append(paths, path)
		}
	}
	cleaned := strings.TrimSpace(sendTagPattern.ReplaceAllString(text, ""))
	return paths, cleaned
}

func SendTaggedFiles(bot sender, conversation chat.ConversationRef, paths []string) []string {
	failures := make([]string, 0)
	for _, path := range paths {
		if err := SendTaggedFile(bot, conversation, path); err != nil {
			log.Printf("send file error for %s: %v", path, err)
			failures = append(failures, "Failed to send file: "+filepath.Base(path))
		}
	}
	return failures
}

func SendTaggedFile(bot sender, conversation chat.ConversationRef, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("empty file path")
	}
	chatID, err := telegramChatID(conversation)
	if err != nil {
		return err
	}
	file := tb.FromDisk(path)
	if IsPhotoPath(path) {
		_, err := bot.Send(tb.ChatID(chatID), &tb.Photo{File: file})
		return err
	}
	_, err = bot.Send(tb.ChatID(chatID), &tb.Document{
		File:     file,
		FileName: filepath.Base(path),
	})
	return err
}

func IsPhotoPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".gif":
		return true
	default:
		return false
	}
}

func emptyResultMessage(backend string) string {
	backend = strings.TrimSpace(backend)
	if backend == "" {
		return "Backend returned an empty response."
	}
	return fmt.Sprintf("Backend %s returned an empty response.", backend)
}

func DeliverJobResult(bot sender, job *PendingJob) error {
	paths, text := SplitResponseFiles(job.Result)
	conversation := conversationFromID(job.ConversationID)
	log.Printf("DeliverJobResult %s: conv=%s result_len=%d text_len=%d files=%d",
		job.ID, conversation.ID(), len(job.Result), len(text), len(paths))
	failures := SendTaggedFiles(bot, conversation, paths)
	if len(failures) > 0 {
		if text != "" {
			text += "\n\n" + strings.Join(failures, "\n")
		} else {
			text = strings.Join(failures, "\n")
		}
	}
	if text == "" && len(paths) == 0 {
		text = emptyResultMessage(job.State.Backend)
		log.Printf("DeliverJobResult %s: result was empty, using fallback message", job.ID)
	}
	err := SendChunked(bot, conversation, text)
	if err != nil {
		log.Printf("DeliverJobResult %s: SendChunked failed: %v", job.ID, err)
	}
	return err
}

func SendImmediate(bot sender, conversation chat.ConversationRef, text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", true
	}

	job := PendingJob{
		ID:             store.NewJobID(),
		ConversationID: conversation.ID(),
		Source:         string(conversation.Provider),
		Status:         "ready",
		Result:         html.EscapeString(text),
	}
	store.WriteJob(job)
	ProcessJob(job, bot, nil, nil)

	delivered := !store.JobExists(job.ID)
	if delivered {
		log.Printf("delivered immediate job %s", job.ID)
	} else {
		log.Printf("queued immediate job %s for retry", job.ID)
	}
	return job.ID, delivered
}

func newTypingStopper(bot sender, conversation chat.ConversationRef, enabled bool) func() {
	if !enabled {
		return func() {}
	}
	stop := StartTyping(bot, conversation)
	var once sync.Once
	return func() {
		once.Do(stop)
	}
}

func telegramDispatchCallbacks(bot sender, job *PendingJob, stopTyping func()) dispatch.Callbacks {
	status := newRunningStatus(bot, job)
	if stopTyping == nil {
		stopTyping = func() {}
	}
	return dispatch.Callbacks{
		OnActivity: func(activity string) {
			status.show(activity)
		},
		OnResult: func(result string) error {
			stopTyping()
			job.Result = result
			return DeliverJobResult(bot, job)
		},
		OnStatusClear: func() {
			stopTyping()
			status.clear()
		},
		OnDone: stopTyping,
	}
}

func ProcessJob(job PendingJob, bot sender, client *oneagent.Client, schedules *scheduler.Store) {
	stopTyping := newTypingStopper(bot, conversationFromID(job.ConversationID), job.Status != "ready" && job.Status != "delivered")
	dispatch.ProcessJob(&job, client, schedules, telegramDispatchCallbacks(bot, &job, stopTyping))
}

func isTelegramJob(job store.PendingJob) bool {
	if job.Source == "subagent" {
		return false
	}
	if chat.ParseConversationID(job.ConversationID).Provider == chat.ProviderTelegram {
		return true
	}
	return job.Source == string(chat.ProviderTelegram)
}

func RecoverPendingJobs(bot sender, client *oneagent.Client, schedules *scheduler.Store) bool {
	maxRecovered := maxTelegramSourceEventID(store.ListJobs())
	recovered := dispatch.RecoverPendingJobs(client, schedules, func(job *store.PendingJob) dispatch.Callbacks {
		stopTyping := newTypingStopper(bot, conversationFromID(job.ConversationID), job.Status != "ready" && job.Status != "delivered")
		return telegramDispatchCallbacks(bot, job, stopTyping)
	}, isTelegramJob)
	if maxRecovered > ReadCursor() {
		WriteCursor(maxRecovered)
	}
	return recovered
}

func DiscardPendingJobs(reason string) bool {
	maxRecovered := maxTelegramSourceEventID(store.ListJobs())
	discarded := dispatch.DiscardPendingJobs(reason, isTelegramJob)
	if maxRecovered > ReadCursor() {
		WriteCursor(maxRecovered)
	}
	return discarded
}

func RetryDeliverableJobs(bot sender, client *oneagent.Client, schedules *scheduler.Store) bool {
	return dispatch.RetryDeliverableJobs(client, schedules, func(job *store.PendingJob) dispatch.Callbacks {
		stopTyping := newTypingStopper(bot, conversationFromID(job.ConversationID), job.Status != "ready" && job.Status != "delivered")
		return telegramDispatchCallbacks(bot, job, stopTyping)
	}, isTelegramJob)
}

func HandleUpdate(u tb.Update, bot fileSender, cfg *store.Config, defaultCWD string, st store.State, client *oneagent.Client) {
	conversation := ConfigConversation(*cfg)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic in handleUpdate: %v", r)
			SendImmediate(bot, conversation, "Internal error — bot recovered.")
		}
	}()

	expectedChatID, err := telegramChatID(conversation)
	if err != nil {
		log.Printf("invalid telegram conversation: %v", err)
		return
	}
	if u.Message == nil || u.Message.Chat.ID != expectedChatID {
		return
	}

	text := u.Message.Text
	if strings.HasPrefix(text, "/") {
		result := chat.HandleInbound(chat.InboundMessage{
			EventID:      strconv.Itoa(u.ID),
			Source:       string(chat.ProviderTelegram),
			Conversation: conversation,
			SenderName:   senderName(u.Message.Sender),
			Text:         text,
		}, chatSettings(cfg), defaultCWD, st, client)
		if result.ImmediateReply != "" {
			SendImmediate(bot, conversation, result.ImmediateReply)
		}
		return
	}

	prompt, tempPath, err := BuildInboundPrompt(bot, u.Message)
	if err != nil {
		log.Printf("message processing error: %v", err)
		SendImmediate(bot, conversation, "Failed to process the incoming media.")
		return
	}
	if prompt == "" {
		return
	}

	log.Printf("message from %s: %s", senderName(u.Message.Sender), prompt)
	result := chat.HandleInbound(chat.InboundMessage{
		EventID:      strconv.Itoa(u.ID),
		Source:       string(chat.ProviderTelegram),
		Conversation: conversation,
		SenderName:   senderName(u.Message.Sender),
		Text:         u.Message.Text,
		Prompt:       prompt,
		TempPath:     tempPath,
	}, chatSettings(cfg), defaultCWD, st, client)
	if result.Job != nil {
		ProcessJob(*result.Job, bot, client, nil)
	}
}
