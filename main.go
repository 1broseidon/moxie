package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	tele "gopkg.in/telebot.v4"
)

type Config struct {
	Token  string `json:"token"`
	ChatID int64  `json:"chat_id"`
}

func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tele")
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

func cursorPath() string {
	return filepath.Join(configDir(), "cursor")
}

func sessionPath() string {
	return filepath.Join(configDir(), "session")
}

func senderName(u *tele.User) string {
	if u == nil {
		return "unknown"
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	return u.FirstName
}

func readSession() string {
	data, err := os.ReadFile(sessionPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func writeSession(id string) {
	os.WriteFile(sessionPath(), []byte(id), 0600)
}

func clearSession() {
	os.Remove(sessionPath())
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
  tele serve [--cwd <dir>] [--model <m>] Long-poll and dispatch to claude CLI`)
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
	cfg, err := loadConfig()
	if err != nil {
		fatal("%v", err)
	}

	bot, err := newBot(cfg)
	if err != nil {
		fatal("bot init failed: %v", err)
	}

	if _, err := bot.Send(tele.ChatID(cfg.ChatID), msg); err != nil {
		fatal("send failed: %v", err)
	}
	fmt.Println("sent")
}

func cmdMessages() {
	cfg, err := loadConfig()
	if err != nil {
		fatal("%v", err)
	}

	format, limit := parseListFlags(2)

	bot, err := newBot(cfg)
	if err != nil {
		fatal("bot init failed: %v", err)
	}

	updates := getUpdates(bot, -limit, 0)
	msgs := extractMessages(updates)
	printMessages(msgs, format)
}

func cmdPoll() {
	cfg, err := loadConfig()
	if err != nil {
		fatal("%v", err)
	}

	format, _ := parseListFlags(2)

	bot, err := newBot(cfg)
	if err != nil {
		fatal("bot init failed: %v", err)
	}

	updates := getUpdates(bot, cursorOffset(), 0)
	msgs := extractMessages(updates)

	if len(msgs) == 0 {
		// Exit silently — no new messages
		return
	}

	// Advance cursor to highest update_id
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

// Backend config — loaded from ~/.config/tele/backends.json

type Backend struct {
	Cmd          []string `json:"cmd"`
	ResumeCmd    []string `json:"resume_cmd,omitempty"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Format       string   `json:"format"` // "json" or "jsonl"
	Result       string   `json:"result"`
	ResultWhen   string   `json:"result_when,omitempty"`
	ResultAppend bool     `json:"result_append,omitempty"`
	Session      string   `json:"session"`
	SessionWhen  string   `json:"session_when,omitempty"`
	DefaultModel string   `json:"default_model,omitempty"`
}

func backendsPath() string {
	return filepath.Join(configDir(), "backends.json")
}

func loadBackends() map[string]Backend {
	data, err := os.ReadFile(backendsPath())
	if err != nil {
		return nil
	}
	var backends map[string]Backend
	json.Unmarshal(data, &backends)
	return backends
}

func statePath() string {
	return filepath.Join(configDir(), "state.json")
}

type State struct {
	Backend string `json:"backend"`
	Model   string `json:"model,omitempty"`
}

func readState() State {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return State{Backend: "claude"}
	}
	var s State
	json.Unmarshal(data, &s)
	if s.Backend == "" {
		s.Backend = "claude"
	}
	return s
}

func writeState(s State) {
	data, _ := json.Marshal(s)
	os.WriteFile(statePath(), data, 0600)
}

func sessionsDir() string {
	return filepath.Join(configDir(), "sessions")
}

func saveSession(id string) {
	if id == "" {
		return
	}
	dir := sessionsDir()
	os.MkdirAll(dir, 0700)
	short := id
	if len(short) > 8 {
		short = short[:8]
	}
	name := readState().Backend + ":" + short
	os.WriteFile(filepath.Join(dir, name), []byte(id), 0600)
}

func listSessions() []string {
	entries, err := os.ReadDir(sessionsDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

func loadSession(name string) (backend, id string) {
	data, err := os.ReadFile(filepath.Join(sessionsDir(), name))
	if err != nil {
		return "", ""
	}
	id = strings.TrimSpace(string(data))
	backend, _, _ = strings.Cut(name, ":")
	if backend == "" {
		backend = "claude"
	}
	return
}

func registerCommands(bot *tele.Bot) {
	cmds := []struct {
		Command     string `json:"command"`
		Description string `json:"description"`
	}{
		{"new", "New session (optionally: /new codex)"},
		{"model", "Show or switch model"},
		{"sessions", "List or switch sessions"},
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

// handleCommand returns a reply string if the command was handled, or "" to pass through.
func handleCommand(text string, backends map[string]Backend) string {
	base, arg := parseCommand(text)

	switch base {
	case "new":
		saveSession(readSession())
		clearSession()
		st := readState()
		if arg != "" {
			if _, ok := backends[arg]; !ok {
				names := make([]string, 0, len(backends))
				for k := range backends {
					names = append(names, k)
				}
				return fmt.Sprintf("Unknown backend: %s\nAvailable: %s", arg, strings.Join(names, ", "))
			}
			st.Backend = arg
			st.Model = ""
			writeState(st)
		}
		return fmt.Sprintf("New %s session.", st.Backend)
	case "model":
		st := readState()
		b := backends[st.Backend]
		if arg == "" {
			model := st.Model
			if model == "" {
				model = b.DefaultModel
			}
			return fmt.Sprintf("Backend: %s\nModel: %s", st.Backend, model)
		}
		st.Model = arg
		writeState(st)
		return "Model set to " + arg
	case "sessions":
		return handleSessions(arg)
	}
	return ""
}

func handleSessions(arg string) string {
	if arg != "" {
		backend, id := loadSession(arg)
		if id == "" {
			return "Session not found: " + arg
		}
		saveSession(readSession())
		writeSession(id)
		st := readState()
		st.Backend = backend
		writeState(st)
		return fmt.Sprintf("Switched to %s session %s", backend, arg)
	}
	names := listSessions()
	if len(names) == 0 {
		return "No saved sessions."
	}
	current := readSession()
	var buf strings.Builder
	for _, n := range names {
		_, id := loadSession(n)
		marker := "  "
		if id == current {
			marker = "> "
		}
		fmt.Fprintf(&buf, "%s%s\n", marker, n)
	}
	buf.WriteString("\n/sessions <name> to switch")
	return buf.String()
}

func parseServeFlags() (cwd, model string) {
	model = "sonnet"
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--cwd":
			if i+1 < len(os.Args) {
				cwd = os.Args[i+1]
				i++
			}
		case "--model":
			if i+1 < len(os.Args) {
				model = os.Args[i+1]
				i++
			}
		}
	}
	return
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
		if _, err := bot.Send(tele.ChatID(chatID), chunk); err != nil {
			log.Printf("send error: %v", err)
		}
	}
}

func handleUpdate(u tele.Update, bot *tele.Bot, cfg Config, cwd string, backends map[string]Backend) {
	if u.Message == nil || u.Message.Chat.ID != cfg.ChatID || u.Message.Text == "" {
		writeCursor(u.ID)
		return
	}

	text := u.Message.Text
	log.Printf("message from %s: %s", senderName(u.Message.Sender), text)

	if strings.HasPrefix(text, "/") {
		if reply := handleCommand(text, backends); reply != "" {
			sendChunked(bot, cfg.ChatID, reply)
			writeCursor(u.ID)
			return
		}
	}

	stop := startTyping(bot, cfg.ChatID)
	response := dispatch(text, cwd, backends)
	stop()

	if response != "" {
		sendChunked(bot, cfg.ChatID, response)
	}
	writeCursor(u.ID)
}

func cmdServe() {
	cfg, err := loadConfig()
	if err != nil {
		fatal("%v", err)
	}

	cwd, _ := parseServeFlags()

	backends := loadBackends()
	if backends == nil {
		fatal("no backends configured — create %s", backendsPath())
	}

	bot, err := newBot(cfg)
	if err != nil {
		fatal("bot init failed: %v", err)
	}

	seedCursor(bot)
	registerCommands(bot)

	st := readState()
	log.Printf("serving — backend=%s, cwd=%s", st.Backend, cwd)

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
			handleUpdate(u, bot, cfg, cwd, backends)
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

func substArgs(tmpl []string, vars map[string]string) []string {
	out := make([]string, 0, len(tmpl))
	for _, t := range tmpl {
		val := t
		for k, v := range vars {
			val = strings.ReplaceAll(val, "{"+k+"}", v)
		}
		if val != "" {
			out = append(out, val)
		}
	}
	return out
}

func dispatch(message, cwd string, backends map[string]Backend) string {
	st := readState()
	b, ok := backends[st.Backend]
	if !ok {
		return "Backend not configured: " + st.Backend
	}

	sessionID := readSession()
	model := st.Model
	if model == "" {
		model = b.DefaultModel
	}

	prompt := message
	if sessionID == "" && b.SystemPrompt != "" {
		prompt = b.SystemPrompt + "\n\n" + message
	}

	vars := map[string]string{
		"prompt":  prompt,
		"model":   model,
		"cwd":     cwd,
		"session": sessionID,
	}

	tmpl := b.Cmd
	if sessionID != "" && len(b.ResumeCmd) > 0 {
		tmpl = b.ResumeCmd
	}
	args := substArgs(tmpl, vars)

	if len(args) == 0 {
		return "Backend command template is empty."
	}

	cmd := exec.Command(args[0], args[1:]...)
	if cwd != "" && !containsVar(b.Cmd, "{cwd}") {
		cmd.Dir = cwd
	}
	cmd.Env = os.Environ()

	var result, newSession string
	var err error

	switch b.Format {
	case "jsonl":
		result, newSession, err = runJSONL(cmd, b)
	default:
		result, newSession, err = runJSON(cmd, b)
	}

	if err != nil {
		log.Printf("%s error: %v", st.Backend, err)
		clearSession()
		return "Error — starting fresh next time."
	}

	if newSession != "" && newSession != sessionID {
		writeSession(newSession)
		log.Printf("session: %s", newSession)
	}

	if result == "" {
		return "Done — nothing to report."
	}
	return result
}

func containsVar(tmpl []string, v string) bool {
	for _, t := range tmpl {
		if strings.Contains(t, v) {
			return true
		}
	}
	return false
}

func runJSON(cmd *exec.Cmd, b Backend) (result, session string, err error) {
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			log.Printf("stderr: %s", exitErr.Stderr)
		}
		return "", "", err
	}

	var blob map[string]any
	if err := json.Unmarshal(out, &blob); err != nil {
		return strings.TrimSpace(string(out)), "", nil
	}

	result, _ = jsonGet(blob, b.Result).(string)
	session, _ = jsonGet(blob, b.Session).(string)
	return result, session, nil
}

func runJSONL(cmd *exec.Cmd, b Backend) (result, session string, err error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", "", err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if b.SessionWhen != "" && matchWhen(line, b.SessionWhen) {
			if v, _ := jsonGet(line, b.Session).(string); v != "" {
				session = v
			}
		}
		if b.ResultWhen != "" && matchWhen(line, b.ResultWhen) {
			if v, _ := jsonGet(line, b.Result).(string); v != "" {
				if b.ResultAppend {
					result += v
				} else {
					result = v
				}
			}
		}
	}

	if err = cmd.Wait(); err != nil {
		if s := stderr.String(); s != "" {
			log.Printf("stderr: %s", strings.TrimSpace(s))
		}
	}
	return result, session, err
}

// jsonGet walks a dot-separated path into a map: "item.text" -> map["item"]["text"]
func jsonGet(m map[string]any, path string) any {
	parts := strings.Split(path, ".")
	var cur any = m
	for _, p := range parts {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[p]
	}
	return cur
}

// matchWhen checks "key=value[&key=value...]" against a JSON line
func matchWhen(m map[string]any, when string) bool {
	for _, cond := range strings.Split(when, "&") {
		k, v, ok := strings.Cut(cond, "=")
		if !ok {
			return false
		}
		got, _ := jsonGet(m, k).(string)
		if got != v {
			return false
		}
	}
	return true
}

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
	default: // md
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

func newBot(cfg Config) (*tele.Bot, error) {
	return tele.NewBot(tele.Settings{
		Token: cfg.Token,
	})
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
