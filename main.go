package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/1broseidon/oneagent"
	tele "gopkg.in/telebot.v4"
)

// --- Telegram config ---

type Config struct {
	Token  string `json:"token"`
	ChatID int64  `json:"chat_id"`
}

var cfgDir string

func configDir() string {
	if cfgDir == "" {
		home, _ := os.UserHomeDir()
		cfgDir = filepath.Join(home, ".config", "tele")
	}
	return cfgDir
}

func loadConfig() (Config, error) {
	path := filepath.Join(configDir(), "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config not found at %s: %w\nRun: tele init", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

// --- Tele state (backend + model selection) ---

type State struct {
	Backend  string `json:"backend"`
	Model    string `json:"model,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
}

func configFile(name string) string {
	return filepath.Join(configDir(), name)
}

func statePath() string {
	return configFile("state.json")
}

func readState() State {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return State{Backend: "claude", ThreadID: "telegram"}
	}
	var s State
	json.Unmarshal(data, &s)
	if s.Backend == "" {
		s.Backend = "claude"
	}
	if s.ThreadID == "" {
		s.ThreadID = "telegram"
	}
	return s
}

func writeState(s State) {
	data, _ := json.Marshal(s)
	os.WriteFile(statePath(), data, 0600)
}

// --- Cursor (Telegram update offset) ---

func cursorPath() string {
	return configFile("cursor")
}

func cursorOffset() int {
	c := readCursor()
	if c > 0 {
		return c + 1
	}
	return 0
}

func readCursor() int {
	data, err := os.ReadFile(cursorPath())
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n
}

func writeCursor(id int) {
	os.WriteFile(cursorPath(), []byte(strconv.Itoa(id)), 0600)
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
	os.MkdirAll(dir, 0700)

	var token string
	var chatID int64

	fmt.Print("Bot token: ")
	fmt.Scanln(&token)
	fmt.Print("Chat ID: ")
	fmt.Scanln(&chatID)

	cfg := Config{Token: token, ChatID: chatID}
	data, _ := json.MarshalIndent(cfg, "", "  ")

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
	_, bot := mustConfigAndBot()
	updates := getUpdates(bot, -limit, 0)
	printMessages(extractMessages(updates), format)
}

func cmdPoll() {
	format, _ := parseListFlags(2)
	_, bot := mustConfigAndBot()
	updates := getUpdates(bot, cursorOffset(), 0)
	msgs := extractMessages(updates)

	if len(msgs) == 0 {
		return
	}

	maxID := 0
	for _, m := range msgs {
		if m.ID > maxID {
			maxID = m.ID
		}
	}
	writeCursor(maxID)
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
			os.Remove(cursorPath())
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
		{"new", "New session (optionally: /new codex)"},
		{"model", "Show or switch model/backend"},
		{"threads", "List saved threads"},
		{"compact", "Summarize old thread turns"},
	}
	data, _ := json.Marshal(map[string]any{"commands": cmds})
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

func handleCommand(text string, client *oneagent.Client) string {
	base, arg := parseCommand(text)
	st := readState()

	switch base {
	case "new":
		if arg != "" {
			if _, ok := client.Backends[arg]; !ok {
				names := make([]string, 0, len(client.Backends))
				for k := range client.Backends {
					names = append(names, k)
				}
				return fmt.Sprintf("Unknown backend: %s\nAvailable: %s", arg, strings.Join(names, ", "))
			}
			st.Backend = arg
			st.Model = ""
		}
		st.ThreadID = fmt.Sprintf("tg-%d", time.Now().Unix())
		writeState(st)
		return fmt.Sprintf("New %s session.", st.Backend)
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
	case "threads":
		return handleThreads(arg, st, client)
	case "compact":
		return compactThread(st, client)
	}
	return ""
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

func compactThread(st State, client *oneagent.Client) string {
	if err := client.CompactThread(st.ThreadID, st.Backend); err != nil {
		return "Compact failed: " + err.Error()
	}
	return "Thread compacted."
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

func buildInboundPrompt(bot *tele.Bot, m *tele.Message) (string, error) {
	if m == nil {
		return "", nil
	}
	if m.Text != "" {
		return m.Text, nil
	}
	if m.Photo != nil {
		// telebot.v4 unmarshals Message.Photo to the largest available size.
		path, err := saveTelegramFile(bot, m.Photo.MediaFile(), "", "photo", ".jpg")
		if err != nil {
			return "", err
		}
		return formatMediaPrompt("a photo", path, m.Caption, "Describe this image"), nil
	}
	if m.Document != nil {
		path, err := saveTelegramFile(bot, m.Document.MediaFile(), m.Document.FileName, "document", ".bin")
		if err != nil {
			return "", err
		}
		name := m.Document.FileName
		if name == "" {
			name = filepath.Base(path)
		}
		return formatMediaPrompt("a file ("+name+")", path, m.Caption, "User sent file: "+name), nil
	}
	if m.Voice != nil {
		path, err := saveTelegramFile(bot, m.Voice.MediaFile(), "", "voice", ".ogg")
		if err != nil {
			return "", err
		}
		return formatMediaPrompt("a voice message", path, m.Caption, "User sent a voice message"), nil
	}
	return "", nil
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

func dispatch(message, cwd string, bot *tele.Bot, chatID int64, client *oneagent.Client) {
	st := readState()
	opts := oneagent.RunOpts{
		Backend:  st.Backend,
		Prompt:   message,
		Model:    st.Model,
		CWD:      cwd,
		ThreadID: st.ThreadID,
	}

	stop := startTyping(bot, chatID)

	emit := func(ev oneagent.StreamEvent) {
		if ev.Type == "activity" && ev.Activity != "" {
			log.Printf("[%s] %s", st.Backend, ev.Activity)
		}
	}

	resp := client.RunWithThreadStream(opts, emit)
	stop()

	if resp.Error != "" {
		log.Printf("%s error: %s", st.Backend, resp.Error)
		sendChunked(bot, chatID, resp.Error)
		return
	}

	result := resp.Result
	paths, text := splitResponseFiles(result)
	failures := sendTaggedFiles(bot, chatID, paths)
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
	sendChunked(bot, chatID, text)
}

// --- Update handler ---

func handleUpdate(u tele.Update, bot *tele.Bot, cfg Config, cwd string, client *oneagent.Client) {
	if u.Message == nil || u.Message.Chat.ID != cfg.ChatID {
		writeCursor(u.ID)
		return
	}

	text := u.Message.Text
	if strings.HasPrefix(text, "/") {
		if reply := handleCommand(text, client); reply != "" {
			sendChunked(bot, cfg.ChatID, reply)
			writeCursor(u.ID)
			return
		}
	}

	prompt, err := buildInboundPrompt(bot, u.Message)
	if err != nil {
		log.Printf("message processing error: %v", err)
		sendChunked(bot, cfg.ChatID, "Failed to process the incoming media.")
		writeCursor(u.ID)
		return
	}
	if prompt == "" {
		writeCursor(u.ID)
		return
	}

	log.Printf("message from %s: %s", senderName(u.Message.Sender), prompt)

	dispatch(prompt, cwd, bot, cfg.ChatID, client)
	writeCursor(u.ID)
}

func cmdServe() {
	cfg, err := loadConfig()
	if err != nil {
		fatal("%v", err)
	}

	cwd := parseServeFlags()

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

	seedCursor(bot)
	registerCommands(bot)

	st := readState()
	log.Printf("serving — backend=%s, thread=%s, cwd=%s", st.Backend, st.ThreadID, cwd)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-sig:
			log.Println("shutting down")
			return
		default:
		}

		for _, u := range getUpdates(bot, cursorOffset(), 30) {
			handleUpdate(u, bot, cfg, cwd, client)
		}
	}
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
		out, _ := json.MarshalIndent(msgs, "", "  ")
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
