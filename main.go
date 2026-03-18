package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/1broseidon/oneagent"
	tele "gopkg.in/telebot.v4"
)

// --- Telegram config ---

type Config struct {
	Token      string            `json:"token"`
	ChatID     int64             `json:"chat_id"`
	Workspaces map[string]string `json:"workspaces,omitempty"`
}

var cfgDir string

func configDir() string {
	if cfgDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fatal("cannot determine home directory: %v", err)
		}
		cfgDir = filepath.Join(home, ".config", "tele")
	}
	return cfgDir
}

func loadConfig() (Config, error) {
	var cfg Config
	if err := readJSON("config.json", &cfg); err != nil {
		return Config{}, fmt.Errorf("config not found: %w\nRun: tele init", err)
	}
	if cfg.Token == "" {
		return Config{}, fmt.Errorf("config missing token\nRun: tele init")
	}
	if cfg.ChatID == 0 {
		return Config{}, fmt.Errorf("config missing chat_id\nRun: tele init")
	}
	if cfg.Workspaces == nil {
		cfg.Workspaces = map[string]string{}
	}
	return cfg, nil
}

func saveConfig(cfg Config) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	check(err)
	check(os.WriteFile(configFile("config.json"), data, 0600))
}

// --- Tele state (backend + model selection) ---

type State struct {
	Backend  string `json:"backend"`
	Model    string `json:"model,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
	CWD      string `json:"cwd,omitempty"`
}

type PendingJob struct {
	UpdateID          int       `json:"update_id"`
	ChatID            int64     `json:"chat_id"`
	Prompt            string    `json:"prompt"`
	CWD               string    `json:"cwd,omitempty"`
	TempPath          string    `json:"temp_path,omitempty"`
	StatusMessageID   int       `json:"status_message_id,omitempty"`
	StatusMessageHTML string    `json:"status_message_html,omitempty"`
	State             State     `json:"state"`
	Status            string    `json:"status"`
	Result            string    `json:"result,omitempty"`
	Updated           time.Time `json:"updated"`
}

func configFile(name string) string {
	return filepath.Join(configDir(), name)
}

func readJSON(name string, v any) error {
	data, err := os.ReadFile(configFile(name))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeJSON(name string, v any) {
	data, err := json.Marshal(v)
	check(err)
	check(os.WriteFile(configFile(name), data, 0600))
}

func readState() State {
	var s State
	readJSON("state.json", &s)
	if s.Backend == "" {
		s.Backend = "claude"
	}
	if s.ThreadID == "" {
		s.ThreadID = "telegram"
	}
	return s
}

func writeState(s State) { writeJSON("state.json", s) }

func jobsDir() string {
	return filepath.Join(configDir(), "jobs")
}

func jobFile(updateID int) string {
	return filepath.Join(jobsDir(), strconv.Itoa(updateID)+".json")
}

func writeJob(job PendingJob) {
	job.Updated = time.Now()
	check(os.MkdirAll(jobsDir(), 0700))
	data, err := json.Marshal(job)
	check(err)
	check(os.WriteFile(jobFile(job.UpdateID), data, 0600))
}

func removeJob(updateID int) {
	err := os.Remove(jobFile(updateID))
	if err != nil && !os.IsNotExist(err) {
		log.Printf("error: remove job %d: %v", updateID, err)
	}
}

func cleanupJobTemp(job PendingJob) {
	if job.TempPath == "" {
		return
	}
	if err := os.Remove(job.TempPath); err != nil && !os.IsNotExist(err) {
		log.Printf("temp file cleanup error for %s: %v", job.TempPath, err)
	}
}

func listJobs() []PendingJob {
	entries, err := os.ReadDir(jobsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		log.Printf("error: read jobs dir: %v", err)
		return nil
	}
	jobs := make([]PendingJob, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(jobsDir(), entry.Name()))
		if err != nil {
			log.Printf("error: read job %s: %v", entry.Name(), err)
			continue
		}
		var job PendingJob
		if err := json.Unmarshal(data, &job); err != nil {
			log.Printf("error: parse job %s: %v", entry.Name(), err)
			continue
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].UpdateID < jobs[j].UpdateID })
	return jobs
}

// --- Cursor (Telegram update offset) ---

func cursorOffset() int {
	if c := readCursor(); c > 0 {
		return c + 1
	}
	return 0
}

func readCursor() int {
	data, err := os.ReadFile(configFile("cursor"))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		log.Printf("corrupt cursor file, resetting: %v", err)
		return 0
	}
	return n
}

func writeCursor(id int) {
	check(os.WriteFile(configFile("cursor"), []byte(strconv.Itoa(id)), 0600))
}

// --- Helpers ---

func senderName(u *tele.User) string {
	if u == nil {
		return "unknown"
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	return u.FirstName
}

const teleSystemPrompt = `You are responding via a Telegram bot. Format all replies using Telegram HTML.
Supported tags: <b>bold</b>, <i>italic</i>, <u>underline</u>, <s>strikethrough</s>, <code>inline code</code>, <pre>code block</pre>, <a href="url">link</a>.
No markdown. No unsupported tags. Keep replies concise.
To send a local file back to Telegram, include <send>/absolute/path/to/file</send> in your reply. The tag is stripped from the visible text and the file is uploaded separately.`

var (
	sendTagPattern  = regexp.MustCompile(`(?s)<send>\s*(.*?)\s*</send>`)
	unsafeFileChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)
	shuttingDown    atomic.Bool
)

func injectSystemPrompt(client *oneagent.Client) {
	for name, b := range client.Backends {
		b.SystemPrompt = teleSystemPrompt
		client.Backends[name] = b
	}
}

func newBot(cfg Config) (*tele.Bot, error) {
	return tele.NewBot(tele.Settings{Token: cfg.Token})
}

func check(err error) {
	if err != nil {
		log.Printf("error: %v", err)
	}
}

func isShutdownError(errText string) bool {
	errText = strings.ToLower(errText)
	return strings.Contains(errText, "signal: terminated") ||
		strings.Contains(errText, "context canceled") ||
		strings.Contains(errText, "interrupted by signal")
}

func compactText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
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
	bot *tele.Bot
	job *PendingJob
}

func (s runningStatus) show(activity string) {
	text := renderActivityHTML(activity)
	if text == s.job.StatusMessageHTML {
		return
	}

	if s.job.StatusMessageID == 0 {
		msg, err := s.bot.Send(tele.ChatID(s.job.ChatID), text, tele.ModeHTML)
		if err != nil {
			log.Printf("status send error: %v", err)
			return
		}
		s.job.StatusMessageID = msg.ID
		s.job.StatusMessageHTML = text
		writeJob(*s.job)
		return
	}

	stored := tele.StoredMessage{MessageID: strconv.Itoa(s.job.StatusMessageID), ChatID: s.job.ChatID}
	if _, err := s.bot.Edit(stored, text, tele.ModeHTML); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
			return
		}
		log.Printf("status edit error for %d: %v", s.job.StatusMessageID, err)
		return
	}
	s.job.StatusMessageHTML = text
	writeJob(*s.job)
}

func (s runningStatus) clear() {
	if s.job.StatusMessageID != 0 {
		stored := tele.StoredMessage{MessageID: strconv.Itoa(s.job.StatusMessageID), ChatID: s.job.ChatID}
		if err := s.bot.Delete(stored); err != nil && !strings.Contains(strings.ToLower(err.Error()), "message to delete not found") {
			log.Printf("status delete error for %d: %v", s.job.StatusMessageID, err)
		}
	}
	s.job.StatusMessageID = 0
	s.job.StatusMessageHTML = ""
	writeJob(*s.job)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// --- CLI entrypoint ---

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "init":
		cmdInit()
	case "send":
		cmdSend()
	case "messages", "msg":
		cmdMessages()
	case "poll":
		cmdPoll()
	case "cursor":
		cmdCursor()
	case "serve":
		cmdServe()
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`tele — Telegram bot CLI

Usage:
  tele init                              Configure bot token and chat ID
  tele send <message>                    Send a message
  tele messages [--json|--raw] [-n N]    List recent messages (default: markdown)
  tele msg                               Alias for messages
  tele poll [--json|--raw]               Show only NEW messages since last poll, advance cursor
  tele cursor                            Show current cursor position
  tele cursor set <update_id>            Manually set cursor
  tele cursor reset                      Reset cursor to 0
  tele serve [--cwd <dir>]               Long-poll and dispatch to agent backends`)
}

func cmdInit() {
	dir := configDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		fatal("failed to create config dir: %v", err)
	}

	var token string
	var chatID int64

	fmt.Print("Bot token: ")
	fmt.Scanln(&token)
	fmt.Print("Chat ID: ")
	fmt.Scanln(&chatID)

	if token == "" {
		fatal("token cannot be empty")
	}
	if chatID == 0 {
		fatal("chat ID cannot be zero")
	}

	cfg := Config{Token: token, ChatID: chatID}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fatal("failed to marshal config: %v", err)
	}

	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		fatal("failed to write config: %v", err)
	}
	fmt.Printf("Config saved to %s\n", path)
}

func cmdSend() {
	if len(os.Args) < 3 {
		fatal("usage: tele send <message>")
	}
	msg := strings.Join(os.Args[2:], " ")
	cfg, bot := mustConfigAndBot()
	if _, err := bot.Send(tele.ChatID(cfg.ChatID), msg); err != nil {
		fatal("send failed: %v", err)
	}
	fmt.Println("sent")
}

func mustConfigAndBot() (Config, *tele.Bot) {
	cfg, err := loadConfig()
	if err != nil {
		fatal("%v", err)
	}
	bot, err := newBot(cfg)
	if err != nil {
		fatal("bot init failed: %v", err)
	}
	return cfg, bot
}

func cmdMessages() {
	format, limit := parseListFlags(2)
	fetchAndPrint(format, -limit, false)
}

func cmdPoll() {
	format, _ := parseListFlags(2)
	fetchAndPrint(format, cursorOffset(), true)
}

func fetchAndPrint(format string, offset int, advanceCursor bool) {
	_, bot := mustConfigAndBot()
	msgs := extractMessages(getUpdates(bot, offset, 0))
	if len(msgs) == 0 {
		return
	}
	if advanceCursor {
		maxID := 0
		for _, m := range msgs {
			if m.ID > maxID {
				maxID = m.ID
			}
		}
		writeCursor(maxID)
	}
	printMessages(msgs, format)
}

func cmdCursor() {
	if len(os.Args) >= 3 {
		switch os.Args[2] {
		case "set":
			if len(os.Args) < 4 {
				fatal("usage: tele cursor set <update_id>")
			}
			n, err := strconv.Atoi(os.Args[3])
			if err != nil {
				fatal("invalid update_id: %s", os.Args[3])
			}
			writeCursor(n)
			fmt.Printf("cursor set to %d\n", n)
			return
		case "reset":
			os.Remove(configFile("cursor"))
			fmt.Println("cursor reset")
			return
		}
	}
	c := readCursor()
	if c == 0 {
		fmt.Println("cursor: not set (will fetch all available)")
	} else {
		fmt.Printf("cursor: %d\n", c)
	}
}

// --- Slash commands ---

func registerCommands(bot *tele.Bot) {
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
	check(err)
	bot.Raw("setMyCommands", json.RawMessage(data))
}

func parseCommand(text string) (base, arg string) {
	cmd := strings.TrimPrefix(text, "/")
	if idx := strings.Index(cmd, "@"); idx >= 0 {
		cmd = cmd[:idx]
	}
	parts := strings.SplitN(cmd, " ", 2)
	base = parts[0]
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

func handleCommand(text string, client *oneagent.Client, cfg *Config) string {
	base, arg := parseCommand(text)
	st := readState()

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

func handleNew(arg string, st State, client *oneagent.Client, cfg *Config) string {
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
				saveConfig(*cfg)
			}
			st.CWD = resolved
		} else {
			return fmt.Sprintf("Unknown backend or workspace: %s", word)
		}
	}
	st.ThreadID = fmt.Sprintf("tg-%d", time.Now().Unix())
	writeState(st)
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

func handleCWD(arg string, st State, cfg *Config) string {
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
		saveConfig(*cfg)
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
			saveConfig(*cfg)
		}
		st.CWD = resolved
		writeState(st)
		return fmt.Sprintf("CWD: %s (%s)", name, st.CWD)
	}
	return "Unknown workspace: " + name + "\n/cwd <name> <path> to create"
}

func switchModel(arg string, st State, client *oneagent.Client) string {
	parts := strings.SplitN(arg, " ", 2)
	if _, ok := client.Backends[parts[0]]; ok {
		st.Backend = parts[0]
		st.Model = ""
		if len(parts) > 1 {
			st.Model = parts[1]
		}
		writeState(st)
		if st.Model != "" {
			return fmt.Sprintf("Switched to %s (%s)", st.Backend, st.Model)
		}
		return "Switched to " + st.Backend
	}
	st.Model = arg
	writeState(st)
	return "Model set to " + arg
}

func handleThreads(arg string, st State, client *oneagent.Client) string {
	if arg != "" {
		st.ThreadID = arg
		writeState(st)
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

// --- Serve loop ---

func parseServeFlags() string {
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--cwd" && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return ""
}

func seedCursor(bot *tele.Bot) {
	if readCursor() != 0 {
		return
	}
	updates := getUpdates(bot, -1, 0)
	if len(updates) > 0 {
		last := updates[len(updates)-1]
		writeCursor(last.ID)
		log.Printf("cursor seeded to %d (skipping old messages)", last.ID)
	}
}

var htmlTagPattern = regexp.MustCompile(`<[^>]*>`)

func sendChunked(bot *tele.Bot, chatID int64, text string) {

	for len(text) > 0 {
		chunk := text
		if len(chunk) > 4000 {
			cut := strings.LastIndex(chunk[:4000], "\n")
			if cut < 0 {
				cut = 4000
			}
			chunk = text[:cut]
			text = text[cut:]
		} else {
			text = ""
		}
		if _, err := bot.Send(tele.ChatID(chatID), chunk, tele.ModeHTML); err != nil {
			log.Printf("send error: %v", err)
			if !strings.Contains(err.Error(), "can't parse entities") {
				continue
			}

			plainChunk := htmlTagPattern.ReplaceAllString(chunk, "")
			if strings.TrimSpace(plainChunk) == "" {
				log.Printf("plain text resend skipped: stripped chunk is empty")
				continue
			}

			if _, plainErr := bot.Send(tele.ChatID(chatID), plainChunk); plainErr != nil {
				log.Printf("plain text resend error: %v", plainErr)
			}
		}
	}
}

func startTyping(bot *tele.Bot, chatID int64) func() {
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

func buildInboundPrompt(bot *tele.Bot, m *tele.Message) (string, string, error) {
	if m == nil {
		return "", "", nil
	}
	if m.Text != "" {
		return m.Text, "", nil
	}

	var file *tele.File
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

	path, err := saveTelegramFile(bot, file, origName, base, ext)
	if err != nil {
		return "", "", err
	}
	if origName != "" {
		kind = "a file (" + origName + ")"
		fallback = "User sent file: " + origName
	}
	return formatMediaPrompt(kind, path, m.Caption, fallback), path, nil
}

func saveTelegramFile(bot *tele.Bot, file *tele.File, originalName, fallbackBase, defaultExt string) (string, error) {
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

	dir := filepath.Join(os.TempDir(), "tele-media")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	dst, err := os.CreateTemp(dir, tempFilePattern(originalName, remoteFile.FilePath, fallbackBase, defaultExt))
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, reader); err != nil {
		return "", fmt.Errorf("save temp file: %w", err)
	}
	return dst.Name(), nil
}

func tempFilePattern(originalName, remotePath, fallbackBase, defaultExt string) string {
	source := originalName
	if source == "" {
		source = remotePath
	}

	ext := strings.ToLower(filepath.Ext(source))
	if ext == "" {
		ext = defaultExt
	}

	base := sanitizeFileStem(strings.TrimSuffix(filepath.Base(source), filepath.Ext(source)), fallbackBase)
	return base + "-*" + ext
}

func sanitizeFileStem(name, fallback string) string {
	cleaned := strings.Trim(unsafeFileChars.ReplaceAllString(name, "_"), "._-")
	if cleaned == "" {
		return fallback
	}
	return cleaned
}

func formatMediaPrompt(kind, path, caption, fallbackRequest string) string {
	line := fmt.Sprintf("User sent %s: %s", kind, path)
	if caption != "" {
		return line + "\nCaption: " + caption
	}
	return line + "\nRequest: " + fallbackRequest
}

func splitResponseFiles(text string) ([]string, string) {
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

func sendTaggedFiles(bot *tele.Bot, chatID int64, paths []string) []string {
	failures := make([]string, 0)
	for _, path := range paths {
		if err := sendTaggedFile(bot, chatID, path); err != nil {
			log.Printf("send file error for %s: %v", path, err)
			failures = append(failures, "Failed to send file: "+filepath.Base(path))
		}
	}
	return failures
}

func sendTaggedFile(bot *tele.Bot, chatID int64, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("empty file path")
	}
	file := tele.FromDisk(path)
	if isPhotoPath(path) {
		_, err := bot.Send(tele.ChatID(chatID), &tele.Photo{File: file})
		return err
	}
	_, err := bot.Send(tele.ChatID(chatID), &tele.Document{
		File:     file,
		FileName: filepath.Base(path),
	})
	return err
}

func isPhotoPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".gif":
		return true
	default:
		return false
	}
}

// --- Dispatch via oneagent ---

func runModel(job *PendingJob, bot *tele.Bot, client *oneagent.Client, status runningStatus) (string, bool) {
	opts := oneagent.RunOpts{
		Backend:  job.State.Backend,
		Prompt:   job.Prompt,
		Model:    job.State.Model,
		CWD:      job.CWD,
		ThreadID: job.State.ThreadID,
		Source:   "telegram",
	}

	stop := startTyping(bot, job.ChatID)
	defer stop()

	emit := func(ev oneagent.StreamEvent) {
		if ev.Type == "activity" && ev.Activity != "" {
			log.Printf("[%s] %s", job.State.Backend, ev.Activity)
			status.show(ev.Activity)
		}
	}

	resp := client.RunWithThreadStream(opts, emit)
	if resp.Error != "" {
		if shuttingDown.Load() && isShutdownError(resp.Error) {
			log.Printf("%s interrupted by shutdown: %s", job.State.Backend, resp.Error)
			return "", true
		}
		log.Printf("%s error: %s", job.State.Backend, resp.Error)
		return resp.Error, false
	}
	return resp.Result, false
}

func deliverJobResult(bot *tele.Bot, job PendingJob) {
	paths, text := splitResponseFiles(job.Result)
	failures := sendTaggedFiles(bot, job.ChatID, paths)
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
		return
	}
	sendChunked(bot, job.ChatID, text)
}

func processJob(job PendingJob, bot *tele.Bot, client *oneagent.Client) {
	status := runningStatus{bot: bot, job: &job}
	if job.Status != "ready" {
		job.Status = "running"
		writeJob(job)
		result, interrupted := runModel(&job, bot, client, status)
		if interrupted {
			return
		}
		job.Result = result
		job.Status = "ready"
		writeJob(job)
	}
	status.clear()
	deliverJobResult(bot, job)
	cleanupJobTemp(job)
	removeJob(job.UpdateID)
}

func canRetryJob(job PendingJob) bool {
	if job.TempPath == "" {
		return true
	}
	if _, err := os.Stat(job.TempPath); err == nil {
		return true
	} else if os.IsNotExist(err) {
		log.Printf("cannot retry job %d: missing temp file %s", job.UpdateID, job.TempPath)
	} else {
		log.Printf("cannot retry job %d: temp file check failed for %s: %v", job.UpdateID, job.TempPath, err)
	}
	return false
}

func recoverPendingJobs(bot *tele.Bot, client *oneagent.Client) bool {
	jobs := listJobs()
	if len(jobs) == 0 {
		return false
	}
	log.Printf("recovering %d pending job(s)", len(jobs))
	maxRecovered := 0
	for _, job := range jobs {
		switch job.Status {
		case "ready":
			log.Printf("replaying ready job %d", job.UpdateID)
			processJob(job, bot, client)
			if job.UpdateID > maxRecovered {
				maxRecovered = job.UpdateID
			}
		case "running":
			if !canRetryJob(job) {
				log.Printf("discarding interrupted job %d; update will be retried", job.UpdateID)
				cleanupJobTemp(job)
				removeJob(job.UpdateID)
				continue
			}
			log.Printf("retrying interrupted job %d", job.UpdateID)
			processJob(job, bot, client)
			if job.UpdateID > maxRecovered {
				maxRecovered = job.UpdateID
			}
		default:
			log.Printf("discarding unknown job state %q for %d", job.Status, job.UpdateID)
			cleanupJobTemp(job)
			removeJob(job.UpdateID)
		}
	}
	if maxRecovered > readCursor() {
		writeCursor(maxRecovered)
	}
	return true
}

// --- Update handler ---

func handleUpdate(u tele.Update, bot *tele.Bot, cfg *Config, defaultCWD string, st State, client *oneagent.Client) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic in handleUpdate: %v", r)
			sendChunked(bot, cfg.ChatID, "Internal error — bot recovered.")
		}
	}()

	if u.Message == nil || u.Message.Chat.ID != cfg.ChatID {
		return
	}

	text := u.Message.Text
	if strings.HasPrefix(text, "/") {
		base, _ := parseCommand(text)
		if !isSupportedCommand(base) {
			sendChunked(bot, cfg.ChatID, "Unknown command. Try /new, /model, /cwd, /threads, or /compact.")
			return
		}
		if reply := handleCommand(text, client, cfg); reply != "" {
			sendChunked(bot, cfg.ChatID, reply)
		}
		return
	}

	prompt, tempPath, err := buildInboundPrompt(bot, u.Message)
	if err != nil {
		log.Printf("message processing error: %v", err)
		sendChunked(bot, cfg.ChatID, "Failed to process the incoming media.")
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
	processJob(PendingJob{
		UpdateID: u.ID,
		ChatID:   cfg.ChatID,
		Prompt:   prompt,
		CWD:      cwd,
		TempPath: tempPath,
		State:    st,
	}, bot, client)
}

func cmdServe() {
	cfg, err := loadConfig()
	if err != nil {
		fatal("%v", err)
	}

	defaultCWD := parseServeFlags()
	if defaultCWD != "" {
		defaultCWD, err = resolveDir(defaultCWD)
		if err != nil {
			fatal("invalid --cwd: %v", err)
		}
	}

	backends, err := oneagent.LoadBackends("")
	if err != nil {
		fatal("no backends: %v", err)
	}

	client := &oneagent.Client{Backends: backends}
	injectSystemPrompt(client)

	bot, err := newBot(cfg)
	if err != nil {
		fatal("bot init failed: %v", err)
	}

	hadPendingJobs := recoverPendingJobs(bot, client)
	if !hadPendingJobs {
		seedCursor(bot)
	}
	registerCommands(bot)

	cursor := readCursor()
	st := readState()
	log.Printf("serving — backend=%s, thread=%s, cwd=%s", st.Backend, st.ThreadID, defaultCWD)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		shuttingDown.Store(true)
		log.Println("shutdown requested; draining current work")
	}()

	offset := func() int {
		if cursor > 0 {
			return cursor + 1
		}
		return 0
	}

	for !shuttingDown.Load() {
		for _, u := range getUpdates(bot, offset(), 30) {
			if shuttingDown.Load() {
				log.Println("shutting down")
				return
			}
			st = readState()
			handleUpdate(u, bot, &cfg, defaultCWD, st, client)
			cursor = u.ID
			writeCursor(cursor)
			if shuttingDown.Load() {
				log.Println("shutting down")
				return
			}
		}
	}
	log.Println("shutting down")
}

// --- Message display (for CLI subcommands) ---

func parseListFlags(startIdx int) (format string, limit int) {
	format = "md"
	limit = 10
	for i := startIdx; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--json":
			format = "json"
		case "--md":
			format = "md"
		case "--raw":
			format = "raw"
		case "-n":
			if i+1 < len(os.Args) {
				n, err := strconv.Atoi(os.Args[i+1])
				if err == nil {
					limit = n
				}
				i++
			}
		}
	}
	return
}

type msgInfo struct {
	ID   int       `json:"id"`
	From string    `json:"from"`
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

func extractMessages(updates []tele.Update) []msgInfo {
	var msgs []msgInfo
	for _, u := range updates {
		if u.Message == nil {
			continue
		}
		m := u.Message
		msgs = append(msgs, msgInfo{
			ID:   u.ID,
			From: senderName(m.Sender),
			Text: m.Text,
			Time: time.Unix(m.Unixtime, 0),
		})
	}
	return msgs
}

func printMessages(msgs []msgInfo, format string) {
	if len(msgs) == 0 {
		fmt.Println("no messages")
		return
	}

	switch format {
	case "json":
		out, err := json.MarshalIndent(msgs, "", "  ")
		check(err)
		fmt.Println(string(out))
	case "raw":
		for _, m := range msgs {
			fmt.Printf("[%s] %s: %s\n", m.Time.Format("Jan 02 15:04"), m.From, m.Text)
		}
	default:
		for _, m := range msgs {
			fmt.Printf("- **%s** (%s): %s\n", m.From, m.Time.Format("Jan 2 3:04pm"), m.Text)
		}
	}
}

func getUpdates(bot *tele.Bot, offset int, timeout int) []tele.Update {
	params := map[string]string{
		"timeout":         strconv.Itoa(timeout),
		"allowed_updates": `["message"]`,
	}
	if offset != 0 {
		params["offset"] = strconv.Itoa(offset)
	}

	data, err := bot.Raw("getUpdates", params)
	if err != nil {
		if timeout > 0 {
			log.Printf("getUpdates error: %v, retrying in 5s", err)
			time.Sleep(5 * time.Second)
			return nil
		}
		fatal("failed to get updates: %v", err)
	}

	var resp struct {
		Result []tele.Update `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		if timeout > 0 {
			log.Printf("parse error: %v", err)
			return nil
		}
		fatal("failed to parse updates: %v", err)
	}
	return resp.Result
}
