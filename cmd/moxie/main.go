package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	botpkg "github.com/1broseidon/moxie/internal/bot"
	"github.com/1broseidon/moxie/internal/dispatch"
	promptpkg "github.com/1broseidon/moxie/internal/prompt"
	"github.com/1broseidon/moxie/internal/scheduler"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
	tb "gopkg.in/telebot.v4"
)

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
	case "schedule":
		cmdSchedule()
	case "threads":
		cmdThreads()
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
	fmt.Println(`moxie — Chat agent CLI

Usage:
  moxie init                              Configure bot token and chat ID
  moxie send <message>                    Send a message
  moxie messages [--json|--raw] [-n N]    List recent messages (default: markdown)
  moxie msg                               Alias for messages
  moxie poll [--json|--raw]               Show only NEW messages since last poll, advance cursor
  moxie cursor                            Show current cursor position
  moxie cursor set <update_id>            Manually set cursor
  moxie cursor reset                      Reset cursor to 0
  moxie schedule <subcommand>             Manage schedules
  moxie threads show <id>                 Show turns for a thread
  moxie serve [--cwd <dir>]               Long-poll and dispatch to agent backends`)
}

func cmdInit() {
	dir := store.ConfigDir()
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

	cfg := store.Config{Token: token, ChatID: chatID}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fatal("failed to marshal config: %v", err)
	}

	path := store.ConfigFile("config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		fatal("failed to write config: %v", err)
	}
	fmt.Printf("Config saved to %s\n", path)
}

func cmdSend() {
	if len(os.Args) < 3 {
		fatal("usage: moxie send <message>")
	}
	msg := strings.Join(os.Args[2:], " ")
	cfg, bot := mustConfigAndBot()
	jobID, delivered := botpkg.SendImmediate(bot, cfg.ChatID, msg)
	if delivered {
		fmt.Println("sent")
		return
	}
	fmt.Printf("queued for retry (job %d)\n", jobID)
}

func mustConfigAndBot() (store.Config, *tb.Bot) {
	cfg, err := store.LoadConfig()
	if err != nil {
		fatal("%v", err)
	}
	bot, err := botpkg.NewBot(cfg)
	if err != nil {
		fatal("bot init failed: %v", err)
	}
	return cfg, bot
}

func cmdMessages() {
	format, limit := parseListFlags(2)
	_, bot := mustConfigAndBot()
	msgs := extractMessages(getUpdates(bot, -limit, 0))
	if len(msgs) == 0 {
		return
	}
	printMessages(msgs, format)
}

func cmdPoll() {
	format, _ := parseListFlags(2)
	_, bot := mustConfigAndBot()
	msgs := extractMessages(getUpdates(bot, store.CursorOffset(), 0))
	if len(msgs) == 0 {
		return
	}
	maxID := 0
	for _, m := range msgs {
		if m.ID > maxID {
			maxID = m.ID
		}
	}
	store.WriteCursor(maxID)
	printMessages(msgs, format)
}

func cmdCursor() {
	if len(os.Args) >= 3 {
		switch os.Args[2] {
		case "set":
			if len(os.Args) < 4 {
				fatal("usage: moxie cursor set <update_id>")
			}
			n, err := strconv.Atoi(os.Args[3])
			if err != nil {
				fatal("invalid update_id: %s", os.Args[3])
			}
			store.WriteCursor(n)
			fmt.Printf("cursor set to %d\n", n)
			return
		case "reset":
			os.Remove(store.ConfigFile("cursor"))
			fmt.Println("cursor reset")
			return
		}
	}
	c := store.ReadCursor()
	if c == 0 {
		fmt.Println("cursor: not set (will fetch all available)")
	} else {
		fmt.Printf("cursor: %d\n", c)
	}
}

func cmdThreads() {
	if len(os.Args) < 4 || os.Args[2] != "show" {
		fmt.Fprintln(os.Stderr, "usage: moxie threads show <id>")
		os.Exit(1)
	}
	id := strings.TrimSpace(os.Args[3])
	thread, err := oneagent.LoadThread(id)
	if err != nil {
		fatal("load thread: %v", err)
	}
	if thread.Summary != "" {
		fmt.Printf("Summary: %s\n\n", thread.Summary)
	}
	if len(thread.NativeSessions) > 0 {
		fmt.Print("Sessions:")
		for backend, sid := range thread.NativeSessions {
			fmt.Printf("  %s=%s", backend, sid)
		}
		fmt.Println()
		fmt.Println()
	}
	if len(thread.Turns) == 0 {
		fmt.Println("no turns")
		return
	}
	for _, t := range thread.Turns {
		ts := t.TS
		if parsed, err := time.Parse(time.RFC3339, t.TS); err == nil {
			ts = parsed.Local().Format("Jan 2 3:04pm")
		}
		fmt.Printf("[%s] %s (%s): %s\n", ts, t.Role, t.Backend, t.Content)
	}
}

func newScheduleStore() *scheduler.Store {
	return scheduler.NewStore(store.ConfigFile("schedules.json"), time.Local)
}

func cmdSchedule() {
	if len(os.Args) < 3 {
		scheduleUsage()
		return
	}

	scheduleStore := newScheduleStore()
	switch os.Args[2] {
	case "add":
		cmdScheduleAdd(scheduleStore, os.Args[3:])
	case "list", "ls":
		cmdScheduleList(scheduleStore)
	case "show":
		cmdScheduleShow(scheduleStore, os.Args[3:])
	case "rm", "delete":
		cmdScheduleDelete(scheduleStore, os.Args[3:])
	case "help", "--help", "-h":
		scheduleUsage()
	default:
		fatal("unknown schedule subcommand: %s", os.Args[2])
	}
}

func scheduleUsage() {
	fmt.Println(`moxie schedule

Usage:
  moxie schedule add --action send --in 5m --text "Call John"
  moxie schedule add --action send --at 2026-03-18T10:00:00-05:00 --text "Call John"
  moxie schedule add --action dispatch --cron "0 1 * * *" --text "Run a security scan"
  moxie schedule list
  moxie schedule show <id>
  moxie schedule rm <id>

Notes:
  --action is required: send or dispatch
  Use exactly one of --in, --at, or --cron
  Dispatch schedules capture backend/model/thread/cwd at creation time unless overridden`)
}

func mustScheduleTrigger(in, at, cronSpec string) scheduler.Trigger {
	count := 0
	trigger := scheduler.TriggerAt
	if strings.TrimSpace(in) != "" {
		count++
	}
	if strings.TrimSpace(at) != "" {
		count++
	}
	if strings.TrimSpace(cronSpec) != "" {
		count++
		trigger = scheduler.TriggerCron
	}
	if count == 0 {
		fatal("missing schedule trigger: use --in, --at, or --cron")
	}
	if count > 1 {
		fatal("use exactly one of --in, --at, or --cron")
	}
	return trigger
}

func applyScheduleAddOverrides(input *scheduler.AddInput, backend, model, thread, cwd string) {
	if trimmed := strings.TrimSpace(backend); trimmed != "" {
		input.Backend = trimmed
	}
	if trimmed := strings.TrimSpace(model); trimmed != "" {
		input.Model = trimmed
	}
	if trimmed := strings.TrimSpace(thread); trimmed != "" {
		input.ThreadID = trimmed
	}
	if strings.TrimSpace(cwd) == "" {
		return
	}
	resolved, err := resolveDir(cwd)
	if err != nil {
		fatal("invalid --cwd: %v", err)
	}
	input.CWD = resolved
}

func applyScheduleActionDefaults(input *scheduler.AddInput) {
	if input.Action != scheduler.ActionSend {
		return
	}
	input.Backend = ""
	input.Model = ""
	input.ThreadID = ""
	input.CWD = ""
}

func cmdScheduleAdd(scheduleStore *scheduler.Store, args []string) {
	fs := flag.NewFlagSet("schedule add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	action := fs.String("action", "", "")
	in := fs.String("in", "", "")
	at := fs.String("at", "", "")
	cronSpec := fs.String("cron", "", "")
	text := fs.String("text", "", "")
	cwdFlag := fs.String("cwd", "", "")
	backendFlag := fs.String("backend", "", "")
	modelFlag := fs.String("model", "", "")
	threadFlag := fs.String("thread", "", "")

	if err := fs.Parse(args); err != nil {
		fatal("usage: moxie schedule add --action <send|dispatch> (--in <duration>|--at <time>|--cron <spec>) --text <text>")
	}
	if fs.NArg() > 0 {
		fatal("unexpected schedule add args: %s", strings.Join(fs.Args(), " "))
	}

	state := store.ReadState()
	input := scheduler.AddInput{
		Trigger:  mustScheduleTrigger(*in, *at, *cronSpec),
		Action:   scheduler.Action(strings.TrimSpace(*action)),
		In:       *in,
		At:       *at,
		Cron:     *cronSpec,
		Text:     *text,
		Backend:  state.Backend,
		Model:    state.Model,
		ThreadID: state.ThreadID,
		CWD:      state.CWD,
	}
	applyScheduleAddOverrides(&input, *backendFlag, *modelFlag, *threadFlag, *cwdFlag)
	applyScheduleActionDefaults(&input)

	sc, err := scheduleStore.Add(input)
	if err != nil {
		fatal("schedule add failed: %v", err)
	}

	fmt.Printf("scheduled %s\n%s\n", sc.ID, renderSchedule(sc))
}

func cmdScheduleList(scheduleStore *scheduler.Store) {
	schedules, err := scheduleStore.List()
	if err != nil {
		fatal("schedule list failed: %v", err)
	}
	if len(schedules) == 0 {
		fmt.Println("no schedules")
		return
	}
	for _, sc := range schedules {
		fmt.Printf("%s  %s  %s\n", sc.ID, formatScheduleHeadline(sc), botpkg.TruncateRunes(sc.Text, 80))
	}
}

func cmdScheduleShow(scheduleStore *scheduler.Store, args []string) {
	if len(args) != 1 {
		fatal("usage: moxie schedule show <id>")
	}
	sc, err := scheduleStore.Get(strings.TrimSpace(args[0]))
	if err != nil {
		if os.IsNotExist(err) {
			fatal("unknown schedule: %s", args[0])
		}
		fatal("schedule show failed: %v", err)
	}
	fmt.Println(renderSchedule(sc))
}

func cmdScheduleDelete(scheduleStore *scheduler.Store, args []string) {
	if len(args) != 1 {
		fatal("usage: moxie schedule rm <id>")
	}
	id := strings.TrimSpace(args[0])
	if err := scheduleStore.Delete(id); err != nil {
		if os.IsNotExist(err) {
			fatal("unknown schedule: %s", id)
		}
		fatal("schedule rm failed: %v", err)
	}
	fmt.Printf("removed %s\n", id)
}

func formatScheduleHeadline(sc scheduler.Schedule) string {
	switch sc.Trigger {
	case scheduler.TriggerAt:
		return fmt.Sprintf("%s at %s", sc.Action, formatScheduleTime(sc.NextRun))
	case scheduler.TriggerCron:
		return fmt.Sprintf("%s cron %s next %s", sc.Action, sc.Cron, formatScheduleTime(sc.NextRun))
	default:
		return fmt.Sprintf("%s next %s", sc.Action, formatScheduleTime(sc.NextRun))
	}
}

func renderSchedule(sc scheduler.Schedule) string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "ID: %s\n", sc.ID)
	fmt.Fprintf(&buf, "Action: %s\n", sc.Action)
	switch sc.Trigger {
	case scheduler.TriggerAt:
		fmt.Fprintf(&buf, "Trigger: at %s\n", formatScheduleTime(sc.At))
	case scheduler.TriggerCron:
		fmt.Fprintf(&buf, "Trigger: cron %s\n", sc.Cron)
	}
	fmt.Fprintf(&buf, "Next run: %s\n", formatScheduleTime(sc.NextRun))
	if !sc.LastRun.IsZero() {
		fmt.Fprintf(&buf, "Last run: %s\n", formatScheduleTime(sc.LastRun))
	}
	if sc.RunningJobID != 0 {
		fmt.Fprintf(&buf, "Running job: %d\n", sc.RunningJobID)
	}
	if sc.Action == scheduler.ActionDispatch {
		fmt.Fprintf(&buf, "Backend: %s\n", sc.Backend)
		if sc.Model != "" {
			fmt.Fprintf(&buf, "Model: %s\n", sc.Model)
		}
		fmt.Fprintf(&buf, "Thread: %s\n", sc.ThreadID)
		if sc.CWD != "" {
			fmt.Fprintf(&buf, "CWD: %s\n", sc.CWD)
		}
	}
	fmt.Fprintf(&buf, "Text: %s\n", sc.Text)
	return strings.TrimSpace(buf.String())
}

func formatScheduleTime(t time.Time) string {
	if t.IsZero() {
		return "(none)"
	}
	return t.In(time.Local).Format("2006-01-02 15:04 MST")
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

func scheduleState(sc scheduler.Schedule) store.State {
	st := store.State{
		Backend:  sc.Backend,
		Model:    sc.Model,
		ThreadID: sc.ThreadID,
		CWD:      sc.CWD,
	}
	if st.Backend == "" {
		st.Backend = "claude"
	}
	if st.ThreadID == "" {
		st.ThreadID = "telegram"
	}
	return st
}

func runDueSchedules(bot *tb.Bot, client *oneagent.Client, schedules *scheduler.Store, chatID int64) {
	due, err := schedules.Due(time.Now())
	if err != nil {
		log.Printf("schedule due check failed: %v", err)
		return
	}
	for _, sc := range due {
		job := botpkg.PendingJob{
			UpdateID:   dispatch.NewSyntheticJobID(),
			ScheduleID: sc.ID,
			ChatID:     chatID,
			CWD:        sc.CWD,
			State:      scheduleState(sc),
		}
		switch sc.Action {
		case scheduler.ActionSend:
			job.Status = "ready"
			job.Result = sc.Text
		case scheduler.ActionDispatch:
			job.Prompt = sc.Text
		default:
			log.Printf("unknown schedule action %q for %s", sc.Action, sc.ID)
			continue
		}

		store.WriteJob(job)
		if _, err := schedules.AttachJob(sc.ID, job.UpdateID); err != nil {
			log.Printf("schedule attach failed for %s: %v", sc.ID, err)
			store.RemoveJob(job.UpdateID)
			continue
		}

		log.Printf("running schedule %s via job %d", sc.ID, job.UpdateID)
		botpkg.ProcessJob(job, bot, client, schedules)
		if dispatch.IsShuttingDown() {
			return
		}
	}
}

func startScheduleLoop(bot *tb.Bot, client *oneagent.Client, schedules *scheduler.Store, chatID int64) {
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		defer ticker.Stop()
		runDueSchedules(bot, client, schedules, chatID)
		for {
			select {
			case <-ticker.C:
				if dispatch.IsShuttingDown() {
					return
				}
				runDueSchedules(bot, client, schedules, chatID)
			}
		}
	}()
}

func startDeliveryRetryLoop(bot *tb.Bot, client *oneagent.Client, schedules *scheduler.Store) {
	ticker := time.NewTicker(15 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if dispatch.IsShuttingDown() {
					return
				}
				botpkg.RetryDeliverableJobs(bot, client, schedules)
			}
		}
	}()
}

func cmdServe() {
	cfg, err := store.LoadConfig()
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
	promptpkg.ApplySystemPrompt(client.Backends)

	bot, err := botpkg.NewBot(cfg)
	if err != nil {
		fatal("bot init failed: %v", err)
	}

	schedules := newScheduleStore()
	if err := schedules.Repair(store.JobExists); err != nil {
		log.Printf("schedule repair failed: %v", err)
	}
	botpkg.RecoverPendingJobs(bot, client, schedules)
	if store.ReadCursor() == 0 {
		botpkg.SeedCursor(bot, getUpdates)
	}
	botpkg.RegisterCommands(bot)
	startScheduleLoop(bot, client, schedules, cfg.ChatID)
	startDeliveryRetryLoop(bot, client, schedules)

	cursor := store.ReadCursor()
	st := store.ReadState()
	log.Printf("serving — backend=%s, thread=%s, cwd=%s", st.Backend, st.ThreadID, defaultCWD)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		dispatch.SetShuttingDown(true)
		log.Println("shutdown requested; draining current work")
	}()

	offset := func() int {
		if cursor > 0 {
			return cursor + 1
		}
		return 0
	}

	for !dispatch.IsShuttingDown() {
		for _, u := range getUpdates(bot, offset(), 30) {
			if dispatch.IsShuttingDown() {
				log.Println("shutting down")
				return
			}
			st = store.ReadState()
			botpkg.HandleUpdate(u, bot, &cfg, defaultCWD, st, client)
			cursor = u.ID
			store.WriteCursor(cursor)
			if dispatch.IsShuttingDown() {
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

func extractMessages(updates []tb.Update) []msgInfo {
	var msgs []msgInfo
	for _, u := range updates {
		if u.Message == nil {
			continue
		}
		m := u.Message
		msgs = append(msgs, msgInfo{
			ID:   u.ID,
			From: botpkg.SenderName(m.Sender),
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
		store.Check(err)
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

func getUpdates(bot *tb.Bot, offset int, timeout int) []tb.Update {
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
		Result []tb.Update `json:"result"`
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
