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

	"github.com/1broseidon/moxie/internal/dispatch"
	promptpkg "github.com/1broseidon/moxie/internal/prompt"
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
	return tb.NewBot(tb.Settings{Token: cfg.Token})
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
	detail := ""
	if len(words) > 1 {
		detail = strings.Join(words[1:], " ")
	}

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
		detail = activity
	default:
		detail = activity
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
}

func (s runningStatus) show(activity string) {
	text := renderActivityHTML(activity)
	if text == s.job.StatusMessageHTML {
		return
	}

	if s.job.StatusMessageID == 0 {
		msg, err := s.bot.Send(tb.ChatID(s.job.ChatID), text, tb.ModeHTML)
		if err != nil {
			log.Printf("status send error: %v", err)
			return
		}
		s.job.StatusMessageID = msg.ID
		s.job.StatusMessageHTML = text
		store.WriteJob(*s.job)
		return
	}

	stored := tb.StoredMessage{MessageID: strconv.Itoa(s.job.StatusMessageID), ChatID: s.job.ChatID}
	if _, err := s.bot.Edit(stored, text, tb.ModeHTML); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
			return
		}
		log.Printf("status edit error for %d: %v", s.job.StatusMessageID, err)
		return
	}
	s.job.StatusMessageHTML = text
	store.WriteJob(*s.job)
}

func (s runningStatus) clear() {
	if s.job.StatusMessageID != 0 {
		stored := tb.StoredMessage{MessageID: strconv.Itoa(s.job.StatusMessageID), ChatID: s.job.ChatID}
		if err := s.bot.Delete(stored); err != nil && !strings.Contains(strings.ToLower(err.Error()), "message to delete not found") {
			log.Printf("status delete error for %d: %v", s.job.StatusMessageID, err)
		}
	}
	s.job.StatusMessageID = 0
	s.job.StatusMessageHTML = ""
	store.WriteJob(*s.job)
}

func RegisterCommands(bot sender) {
	cmds := []struct {
		Command     string `json:"command"`
		Description string `json:"description"`
	}{
		{"new", "New thread (/new [backend] [workspace])"},
		{"model", "Show or switch model/backend"},
		{"cwd", "Show, switch, or save workspace"},
		{"threads", "List saved threads"},
		{"compact", "Summarize old thread turns"},
	}
	data, err := json.Marshal(map[string]any{"commands": cmds})
	store.Check(err)
	bot.Raw("setMyCommands", json.RawMessage(data))
}

func HandleCommand(text string, client *oneagent.Client, cfg *store.Config) string {
	base, arg := parseCommand(text)
	st := store.ReadState()

	switch base {
	case "new":
		return handleNew(arg, st, client, cfg)
	case "model":
		if arg == "" {
			b := client.Backends[st.Backend]
			model := st.Model
			if model == "" {
				model = b.DefaultModel
			}
			return fmt.Sprintf("Backend: %s\nModel: %s", st.Backend, model)
		}
		return switchModel(arg, st, client)
	case "cwd":
		return handleCWD(arg, st, cfg)
	case "threads":
		return handleThreads(arg, st, client)
	case "compact":
		if err := client.CompactThread(st.ThreadID, st.Backend); err != nil {
			return "Compact failed: " + err.Error()
		}
		return "Thread compacted."
	}
	return ""
}

func parseCommand(text string) (base, arg string) {
	cmd := strings.TrimPrefix(text, "/")
	parts := strings.SplitN(cmd, " ", 2)
	base = parts[0]
	if idx := strings.Index(base, "@"); idx >= 0 {
		base = base[:idx]
	}
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	return
}

func isSupportedCommand(name string) bool {
	switch name {
	case "new", "model", "cwd", "threads", "compact":
		return true
	default:
		return false
	}
}

func handleNew(arg string, st store.State, client *oneagent.Client, cfg *store.Config) string {
	for _, word := range strings.Fields(arg) {
		if _, ok := client.Backends[word]; ok {
			st.Backend = word
			st.Model = ""
		} else if path, ok := cfg.Workspaces[word]; ok {
			resolved, err := resolveDir(path)
			if err != nil {
				return fmt.Sprintf("Workspace %s is invalid: %v", word, err)
			}
			if path != resolved {
				cfg.Workspaces[word] = resolved
				store.SaveConfig(*cfg)
			}
			st.CWD = resolved
		} else {
			return fmt.Sprintf("Unknown backend or workspace: %s", word)
		}
	}
	st.ThreadID = fmt.Sprintf("tg-%d", time.Now().Unix())
	store.WriteState(st)
	cwd := st.CWD
	if cwd == "" {
		cwd = "(default)"
	}
	return fmt.Sprintf("New %s session in %s.", st.Backend, cwd)
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Printf("error: cannot expand ~: %v", err)
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func resolveDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path cannot be empty")
	}
	resolved := expandHome(path)
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("access path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", abs)
	}
	return abs, nil
}

func handleCWD(arg string, st store.State, cfg *store.Config) string {
	if arg == "" {
		cwd := st.CWD
		if cwd == "" {
			cwd = "(default from --cwd flag)"
		}
		var buf strings.Builder
		fmt.Fprintf(&buf, "CWD: %s\n\nWorkspaces:\n", cwd)
		for name, path := range cfg.Workspaces {
			fmt.Fprintf(&buf, "  %s → %s\n", name, path)
		}
		if len(cfg.Workspaces) == 0 {
			buf.WriteString("  (none)\n")
		}
		buf.WriteString("\n/cwd <name> to switch\n/cwd <name> <path> to save")
		return buf.String()
	}
	parts := strings.SplitN(arg, " ", 2)
	if len(parts) == 2 {
		name := strings.TrimSpace(parts[0])
		if name == "" {
			return "Workspace name cannot be empty."
		}
		resolved, err := resolveDir(parts[1])
		if err != nil {
			return "Invalid workspace path: " + err.Error()
		}
		cfg.Workspaces[name] = resolved
		store.SaveConfig(*cfg)
		return fmt.Sprintf("Workspace %s → %s", name, resolved)
	}
	name := strings.TrimSpace(parts[0])
	if path, ok := cfg.Workspaces[name]; ok {
		resolved, err := resolveDir(path)
		if err != nil {
			return fmt.Sprintf("Workspace %s is invalid: %v", name, err)
		}
		if path != resolved {
			cfg.Workspaces[name] = resolved
			store.SaveConfig(*cfg)
		}
		st.CWD = resolved
		store.WriteState(st)
		return fmt.Sprintf("CWD: %s (%s)", name, st.CWD)
	}
	return "Unknown workspace: " + name + "\n/cwd <name> <path> to create"
}

func switchModel(arg string, st store.State, client *oneagent.Client) string {
	parts := strings.SplitN(arg, " ", 2)
	if _, ok := client.Backends[parts[0]]; ok {
		st.Backend = parts[0]
		st.Model = ""
		if len(parts) > 1 {
			st.Model = parts[1]
		}
		store.WriteState(st)
		if st.Model != "" {
			return fmt.Sprintf("Switched to %s (%s)", st.Backend, st.Model)
		}
		return "Switched to " + st.Backend
	}
	st.Model = arg
	store.WriteState(st)
	return "Model set to " + arg
}

func handleThreads(arg string, st store.State, client *oneagent.Client) string {
	if arg != "" {
		st.ThreadID = arg
		store.WriteState(st)
		return "Switched to thread " + arg
	}
	ids, err := client.ListThreads()
	if err != nil || len(ids) == 0 {
		return "No saved threads."
	}
	var buf strings.Builder
	for _, id := range ids {
		marker := "  "
		if id == st.ThreadID {
			marker = "> "
		}
		fmt.Fprintf(&buf, "%s%s\n", marker, id)
	}
	buf.WriteString("\n/threads <name> to switch")
	return buf.String()
}

func SeedCursor(bot *tb.Bot, fetchUpdates func(*tb.Bot, int, int) []tb.Update) {
	if store.ReadCursor() != 0 {
		return
	}
	updates := fetchUpdates(bot, -1, 0)
	if len(updates) > 0 {
		last := updates[len(updates)-1]
		store.WriteCursor(last.ID)
		log.Printf("cursor seeded to %d (skipping old messages)", last.ID)
	}
}

func SendChunked(bot sender, chatID int64, text string) error {
	sentAny := false
	var firstErr error

	for len(text) > 0 {
		chunk := text
		if len(chunk) > 4000 {
			cut := strings.LastIndex(chunk[:4000], "\n")
			if cut <= 0 {
				cut = 4000
			}
			chunk = text[:cut]
			text = text[cut:]
		} else {
			text = ""
		}
		text = strings.TrimPrefix(text, "\n")

		if _, err := bot.Send(tb.ChatID(chatID), chunk, tb.ModeHTML); err != nil {
			log.Printf("send error: %v", err)
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

			if _, plainErr := bot.Send(tb.ChatID(chatID), plainChunk); plainErr != nil {
				log.Printf("plain text resend error: %v", plainErr)
				if firstErr == nil {
					firstErr = plainErr
				}
				continue
			}
			sentAny = true
			continue
		}
		sentAny = true
	}

	if !sentAny && firstErr != nil {
		return firstErr
	}
	return nil
}

func StartTyping(bot sender, chatID int64) func() {
	done := make(chan struct{})
	go func() {
		for {
			bot.Raw("sendChatAction", map[string]string{
				"chat_id": strconv.FormatInt(chatID, 10),
				"action":  "typing",
			})
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

	path, err := SaveTelegramFile(bot, file, origName, base, ext)
	if err != nil {
		return "", "", err
	}
	if origName != "" {
		kind = "a file (" + origName + ")"
		fallback = "User sent file: " + origName
	}
	return promptpkg.FormatMediaPrompt(kind, path, m.Caption, fallback), path, nil
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
	defer reader.Close()

	dir := filepath.Join(os.TempDir(), "moxie-media")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	dst, err := os.CreateTemp(dir, TempFilePattern(originalName, remoteFile.FilePath, fallbackBase, defaultExt))
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer dst.Close()

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

func SendTaggedFiles(bot sender, chatID int64, paths []string) []string {
	failures := make([]string, 0)
	for _, path := range paths {
		if err := SendTaggedFile(bot, chatID, path); err != nil {
			log.Printf("send file error for %s: %v", path, err)
			failures = append(failures, "Failed to send file: "+filepath.Base(path))
		}
	}
	return failures
}

func SendTaggedFile(bot sender, chatID int64, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("empty file path")
	}
	file := tb.FromDisk(path)
	if IsPhotoPath(path) {
		_, err := bot.Send(tb.ChatID(chatID), &tb.Photo{File: file})
		return err
	}
	_, err := bot.Send(tb.ChatID(chatID), &tb.Document{
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

func DeliverJobResult(bot sender, job *PendingJob) error {
	paths, text := SplitResponseFiles(job.Result)
	failures := SendTaggedFiles(bot, job.ChatID, paths)
	if len(failures) > 0 {
		if text != "" {
			text += "\n\n" + strings.Join(failures, "\n")
		} else {
			text = strings.Join(failures, "\n")
		}
	}
	if text == "" && len(paths) == 0 {
		text = "Done — nothing to report."
	}
	if text == "" {
		return nil
	}
	return SendChunked(bot, job.ChatID, text)
}

func SendImmediate(bot sender, chatID int64, text string) (int, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, true
	}

	job := PendingJob{
		UpdateID: dispatch.NewSyntheticJobID(),
		ChatID:   chatID,
		Status:   "ready",
		Result:   text,
	}
	store.WriteJob(job)
	ProcessJob(job, bot, nil, nil)

	delivered := !store.JobExists(job.UpdateID)
	if delivered {
		log.Printf("delivered immediate job %d", job.UpdateID)
	} else {
		log.Printf("queued immediate job %d for retry", job.UpdateID)
	}
	return job.UpdateID, delivered
}

func newTypingStopper(bot sender, chatID int64, enabled bool) func() {
	if !enabled {
		return func() {}
	}
	stop := StartTyping(bot, chatID)
	var once sync.Once
	return func() {
		once.Do(stop)
	}
}

func telegramDispatchCallbacks(bot sender, job *PendingJob, stopTyping func()) dispatch.Callbacks {
	status := runningStatus{bot: bot, job: job}
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
	stopTyping := newTypingStopper(bot, job.ChatID, job.Status != "ready" && job.Status != "delivered")
	dispatch.ProcessJob(&job, client, schedules, telegramDispatchCallbacks(bot, &job, stopTyping))
}

func RecoverPendingJobs(bot sender, client *oneagent.Client, schedules *scheduler.Store) bool {
	return dispatch.RecoverPendingJobs(client, schedules, func(job *store.PendingJob) dispatch.Callbacks {
		stopTyping := newTypingStopper(bot, job.ChatID, job.Status != "ready" && job.Status != "delivered")
		return telegramDispatchCallbacks(bot, job, stopTyping)
	})
}

func RetryDeliverableJobs(bot sender, client *oneagent.Client, schedules *scheduler.Store) bool {
	return dispatch.RetryDeliverableJobs(client, schedules, func(job *store.PendingJob) dispatch.Callbacks {
		stopTyping := newTypingStopper(bot, job.ChatID, job.Status != "ready" && job.Status != "delivered")
		return telegramDispatchCallbacks(bot, job, stopTyping)
	})
}

func HandleUpdate(u tb.Update, bot fileSender, cfg *store.Config, defaultCWD string, st store.State, client *oneagent.Client) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic in handleUpdate: %v", r)
			SendImmediate(bot, cfg.ChatID, "Internal error — bot recovered.")
		}
	}()

	if u.Message == nil || u.Message.Chat.ID != cfg.ChatID {
		return
	}

	text := u.Message.Text
	if strings.HasPrefix(text, "/") {
		base, _ := parseCommand(text)
		if !isSupportedCommand(base) {
			SendImmediate(bot, cfg.ChatID, "Unknown command. Try /new, /model, /cwd, /threads, or /compact.")
			return
		}
		if reply := HandleCommand(text, client, cfg); reply != "" {
			SendImmediate(bot, cfg.ChatID, reply)
		}
		return
	}

	prompt, tempPath, err := BuildInboundPrompt(bot, u.Message)
	if err != nil {
		log.Printf("message processing error: %v", err)
		SendImmediate(bot, cfg.ChatID, "Failed to process the incoming media.")
		return
	}
	if prompt == "" {
		return
	}

	log.Printf("message from %s: %s", senderName(u.Message.Sender), prompt)

	cwd := st.CWD
	if cwd == "" {
		cwd = defaultCWD
	}
	ProcessJob(PendingJob{
		UpdateID: u.ID,
		ChatID:   cfg.ChatID,
		Prompt:   prompt,
		CWD:      cwd,
		TempPath: tempPath,
		State:    st,
	}, bot, client, nil)
}
