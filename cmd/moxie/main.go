package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	botpkg "github.com/1broseidon/moxie/internal/bot"
	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/scheduler"
	slackpkg "github.com/1broseidon/moxie/internal/slack"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
	tb "gopkg.in/telebot.v4"
)

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// --- CLI entrypoint ---

var commandHandlers = map[string]func(){
	"init":     cmdInit,
	"send":     cmdSend,
	"messages": cmdMessages,
	"msg":      cmdMessages,
	"poll":     cmdPoll,
	"cursor":   cmdCursor,
	"schedule": cmdSchedule,
	"subagent": cmdSubagent,
	"result":   cmdResult,
	"threads":  cmdThreads,
	"service":  cmdService,
	"serve":    cmdServe,
	"help":     usage,
	"--help":   usage,
	"-h":       usage,
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	if handler, ok := commandHandlers[os.Args[1]]; ok {
		handler()
		return
	}

	fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
	usage()
	os.Exit(1)
}

func usage() {
	fmt.Println(`moxie — Chat agent CLI

Usage:
  moxie init                              Configure bot token and chat ID
  moxie send <message>                    Send a message
  moxie messages [--json|--raw] [-n N]    List recent messages (default: markdown)
  moxie msg                               Alias for messages
  moxie schedule <subcommand>             Manage schedules
  moxie subagent --backend <name> --text <task>  Delegate work to a background agent
  moxie result <subcommand>               Retrieve subagent results
  moxie threads show <id>                 Show turns for a thread
  moxie service <subcommand>              Control the background service
  moxie serve [--cwd <dir>] [--transport <telegram|slack>]  Run configured chat transports and dispatch to agent backends`)
}

const defaultServiceUnit = "moxie-serve.service"
const defaultLaunchdLabel = "io.github.1broseidon.moxie"

var launchdReloadSignal os.Signal = syscall.Signal(1)

func serviceUsage() {
	fmt.Println(`moxie service — control the background service

Usage:
  moxie service install [--cwd <dir>] [--transport <telegram|slack>]
  moxie service uninstall
  moxie service start
  moxie service stop
  moxie service restart
  moxie service reload
  moxie service status

Notes:
  Linux uses systemd user services
  macOS uses launchd with ~/Library/LaunchAgents/io.github.1broseidon.moxie.plist`)
}

func cmdService() {
	if len(os.Args) < 3 {
		serviceUsage()
		return
	}
	switch os.Args[2] {
	case "install":
		cmdServiceInstall(os.Args[3:])
	case "uninstall":
		cmdServiceUninstall()
	case "start", "stop", "restart", "reload", "status":
		cmdServiceControl(os.Args[2])
	default:
		serviceUsage()
	}
}

func cmdServiceControl(action string) {
	switch runtime.GOOS {
	case "linux":
		runSystemdUserAction(action)
	case "darwin":
		runLaunchdUserAction(action)
	default:
		fatal("moxie service %s is not implemented for %s yet; use the platform service manager directly", action, runtime.GOOS)
	}
}

func runSystemdUserAction(action string) {
	cmd := exec.Command("systemctl", "--user", action, defaultServiceUnit)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		fatal("systemctl --user %s %s failed: %v", action, defaultServiceUnit, err)
	}
}

func runLaunchdUserAction(action string) {
	plist := launchdPlistPath()
	target := launchdServiceTarget(os.Getuid())
	domain := launchdDomainTarget(os.Getuid())

	switch action {
	case "start":
		requireLaunchdPlist(plist)
		if launchdServiceLoaded(target) {
			runLaunchctl("kickstart", target)
			return
		}
		runLaunchctl("bootstrap", domain, plist)
	case "stop":
		requireLaunchdPlist(plist)
		runLaunchctl("bootout", domain, plist)
	case "restart":
		requireLaunchdPlist(plist)
		if launchdServiceLoaded(target) {
			runLaunchctl("kickstart", "-k", target)
			return
		}
		runLaunchctl("bootstrap", domain, plist)
	case "reload":
		pid, err := launchdServicePID(target)
		if err != nil {
			fatal("%v", err)
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			fatal("failed to resolve %s (pid %d): %v", target, pid, err)
		}
		if err := proc.Signal(launchdReloadSignal); err != nil {
			fatal("failed to signal %s (pid %d): %v", target, pid, err)
		}
	case "status":
		runLaunchctl("print", target)
	default:
		fatal("unsupported service action: %s", action)
	}
}

func launchdPlistPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal("resolve home dir: %v", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", defaultLaunchdLabel+".plist")
}

func launchdDomainTarget(uid int) string {
	return fmt.Sprintf("gui/%d", uid)
}

func launchdServiceTarget(uid int) string {
	return launchdDomainTarget(uid) + "/" + defaultLaunchdLabel
}

func requireLaunchdPlist(path string) {
	if _, err := os.Stat(path); err == nil {
		return
	}
	fatal("launchd plist not found: %s\nCreate a LaunchAgent at that path, then rerun moxie service", path)
}

func runLaunchctl(args ...string) {
	cmd := exec.Command("launchctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		fatal("launchctl %s failed: %v", strings.Join(args, " "), err)
	}
}

func launchdServiceLoaded(target string) bool {
	cmd := exec.Command("launchctl", "print", target)
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

func launchdServicePID(target string) (int, error) {
	out, err := exec.Command("launchctl", "print", target).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return 0, fmt.Errorf("launchctl print %s failed: %s", target, msg)
		}
		return 0, fmt.Errorf("launchctl print %s failed: %w", target, err)
	}
	pid, ok := parseLaunchdPID(string(out))
	if !ok {
		return 0, fmt.Errorf("could not determine pid for %s", target)
	}
	return pid, nil
}

func parseLaunchdPID(output string) (int, bool) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "pid = ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "pid = "))
		if len(fields) == 0 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err == nil && pid > 0 {
			return pid, true
		}
	}
	return 0, false
}

func cmdInit() {
	dir := store.ConfigDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		fatal("failed to create config dir: %v", err)
	}

	reader := bufio.NewReader(os.Stdin)

	token := promptRequiredLine(reader, "Bot token: ")
	chatIDText := promptRequiredLine(reader, "Chat ID: ")
	chatID, err := strconv.ParseInt(chatIDText, 10, 64)
	if err != nil {
		fatal("invalid chat ID: %s", chatIDText)
	}

	if token == "" {
		fatal("token cannot be empty")
	}
	if chatID == 0 {
		fatal("chat ID cannot be zero")
	}

	cfg := store.Config{
		Channels: map[string]store.ChannelConfig{
			"telegram": {
				Provider:  "telegram",
				Token:     token,
				ChannelID: strconv.FormatInt(chatID, 10),
			},
		},
		Workspaces: map[string]string{},
	}
	store.SaveConfig(cfg)
	path := store.ConfigFile("config.json")
	fmt.Printf("Config saved to %s\n", path)

	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return
	}
	if !promptYesNo(reader, "Install and start as a background service? [y/N]: ", false) {
		return
	}

	defaultCWD, err := os.Getwd()
	if err != nil {
		defaultCWD = ""
	}
	cwd := promptLine(reader, fmt.Sprintf("Service working directory [%s]: ", defaultCWD), defaultCWD)

	opts := serviceInstallOptions{cwd: cwd}
	path, err = installService(opts)
	if err != nil {
		fatal("service install failed: %v", err)
	}
	fmt.Printf("Service definition written to %s\n", path)
	cmdServiceControl("start")
	fmt.Println("Service started")
}

func cmdSend() {
	msg := strings.TrimSpace(joinArgsExcludingTransport(2))
	if msg == "" {
		fatal("usage: moxie send <message>")
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		fatal("%v", err)
	}
	transport, err := chooseServeTransport(cfg, parseTransportFlag(2))
	if err != nil {
		fatal("%v", err)
	}

	switch transport {
	case "telegram":
		bot, err := botpkg.NewBot(cfg)
		if err != nil {
			fatal("bot init failed: %v", err)
		}
		jobID, delivered := botpkg.SendImmediate(bot, botpkg.ConfigConversation(cfg), msg)
		if delivered {
			fmt.Println("sent")
			return
		}
		fmt.Printf("queued for retry (job %s)\n", jobID)
	case "slack":
		conversation := slackDefaultConversation(cfg)
		if conversation.ChannelID == "" {
			fatal("slack send requires channels.slack.channel_id")
		}
		adapter, err := slackpkg.New(&cfg, "", nil, nil)
		if err != nil {
			fatal("slack init failed: %v", err)
		}
		jobID, delivered := slackpkg.SendImmediate(adapter.API(), conversation, msg)
		if delivered {
			fmt.Println("sent")
			return
		}
		fmt.Printf("queued for retry (job %s)\n", jobID)
	default:
		fatal("unsupported transport: %s", transport)
	}
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
	msgs := extractMessages(getUpdates(bot, botpkg.CursorOffset(), 0))
	if len(msgs) == 0 {
		return
	}
	maxID := 0
	for _, m := range msgs {
		if m.ID > maxID {
			maxID = m.ID
		}
	}
	botpkg.WriteCursor(maxID)
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
			botpkg.WriteCursor(n)
			fmt.Printf("cursor set to %d\n", n)
			return
		case "reset":
			if err := os.Remove(store.ConfigFile("telegram-cursor")); err != nil && !os.IsNotExist(err) {
				fatal("failed to reset cursor: %v", err)
			}
			fmt.Println("cursor reset")
			return
		}
	}
	c := botpkg.ReadCursor()
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
	fmt.Println(`moxie schedule — create and manage scheduled messages and tasks

Usage:
  moxie schedule add [flags]
  moxie schedule list
  moxie schedule show <id>
  moxie schedule rm <id>

Flags for add:
  --transport <telegram|slack>                Use the configured default conversation for one transport
  --conversation <provider:channel[:thread]>  Target a specific provider conversation directly
  --action <send|dispatch>                    Send a fixed message or dispatch agent work
  --text <text>                               Message text or dispatch task
  --in <duration>                             Relative one-shot schedule like 5m, 2h, or 1d2h30m
  --at <RFC3339 time>                         Exact one-shot timestamp with offset
  --cron <spec>                               Recurring cron schedule
  --backend <name>                            Override captured backend for dispatch schedules
  --model <name>                              Override captured model for dispatch schedules
  --thread <id>                               Override captured thread for dispatch schedules
  --cwd <dir>                                 Override captured working directory for dispatch schedules

Notes:
  Use exactly one of --in, --at, or --cron
  Use --conversation to target a specific provider conversation, or --transport to target the configured default conversation for one transport
  Dispatch schedules capture backend/model/thread/cwd at creation time

When to use:
  Only when explicitly asked to create, inspect, modify, or delete schedules
  Prefer --in for relative times, --at for exact timestamps, and --cron for recurring schedules
  Use action send for fixed messages and action dispatch for agent work

Examples:
  moxie schedule add --transport telegram --action send --in 5m --text "Call John"
  moxie schedule add --conversation slack:C123:1710000000.100 --action send --at 2026-03-18T10:00:00-05:00 --text "Call John"
  moxie schedule add --transport slack --action dispatch --cron "0 1 * * *" --text "Run a security scan"
  moxie schedule list
  moxie schedule rm sch_123`)
}

func subagentUsage() {
	fmt.Println(`moxie subagent — delegate a task to a background agent

Usage:
  moxie subagent --backend <name> --text <task>

Flags:
  --backend <name>            Required backend to run the delegated task
  --text <task>               Required self-contained task description
  --context-budget <n>        Context budget for compiled parent context (default 8192)
  --cwd <dir>                 Override working directory for the subagent
  --model <name>              Optional model override for the subagent backend
  --parent-job <id>           Explicit parent dispatch job to attach to

When to use:
  Only when delegating a distinct self-contained task to another backend
  Do not use it for simple questions or work you can handle directly
  The subagent runs asynchronously with context from the parent conversation
  Results are delivered back when complete
  You can continue working while the subagent runs

Examples:
  moxie subagent --backend codex --text "Audit the scheduler retry logic"
  moxie subagent --backend gemini --context-budget 16384 --text "Summarize the Slack delivery edge cases"`)
}

func resultUsage() {
	fmt.Println(`moxie result — retrieve metadata and results from previous subagent runs

Usage:
  moxie result list [--limit <n>]
  moxie result show <id>
  moxie result search <query>

Subcommands:
  list              List recent subagent artifacts (default last 20)
  show <id>         Show artifact metadata and the thread ID containing the full result
  search <query>    Search artifact tasks for a substring

Flags for list:
  --limit <n>       Number of artifacts to show (default 20)

When to use:
  Use to find or reference the output of a previous subagent run
  Every completed subagent produces a lightweight artifact with metadata
  The artifact links to the thread where the full conversation is stored
  Use search to find a specific result without knowing the artifact ID

Examples:
  moxie result list
  moxie result list --limit 5
  moxie result show art-1773971234567890
  moxie result search "security audit"`)
}

func cmdResult() {
	if len(os.Args) < 3 {
		resultUsage()
		return
	}
	sub := os.Args[2]
	if sub == "help" || sub == "--help" || sub == "-h" {
		resultUsage()
		return
	}
	switch sub {
	case "list":
		cmdResultList(os.Args[3:])
	case "show":
		cmdResultShow(os.Args[3:])
	case "search":
		cmdResultSearch(os.Args[3:])
	default:
		resultUsage()
	}
}

func cmdResultList(args []string) {
	limit := 20
	for i := 0; i < len(args); i++ {
		if args[i] == "--limit" && i+1 < len(args) {
			if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
				limit = n
			}
			i++
		}
	}
	artifacts := store.ListArtifacts()
	if len(artifacts) == 0 {
		fmt.Println("No artifacts.")
		return
	}
	if len(artifacts) > limit {
		artifacts = artifacts[:limit]
	}
	for _, a := range artifacts {
		task := a.Task
		if len(task) > 80 {
			task = task[:80] + "..."
		}
		fmt.Printf("%s  %-8s  %s  %s\n", a.ID, a.Backend, a.Created.Format("2006-01-02 15:04"), task)
	}
}

func cmdResultShow(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: moxie result show <id>")
		os.Exit(1)
	}
	a, ok := store.ReadArtifact(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "artifact not found: %s\n", args[0])
		os.Exit(1)
	}
	fmt.Printf("ID:        %s\n", a.ID)
	fmt.Printf("Job:       %s\n", a.JobID)
	fmt.Printf("Backend:   %s\n", a.Backend)
	fmt.Printf("Thread:    %s\n", a.ThreadID)
	if a.ParentJob != "" {
		fmt.Printf("Parent:    %s\n", a.ParentJob)
	}
	fmt.Printf("Created:   %s\n", a.Created.Format(time.RFC3339))
	fmt.Printf("Task:      %s\n", a.Task)
}

func cmdResultSearch(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: moxie result search <query>")
		os.Exit(1)
	}
	query := strings.ToLower(strings.Join(args, " "))
	artifacts := store.ListArtifacts()
	found := false
	for _, a := range artifacts {
		if strings.Contains(strings.ToLower(a.Task), query) {
			task := a.Task
			if len(task) > 80 {
				task = task[:80] + "..."
			}
			fmt.Printf("%s  %-8s  %s  %s\n", a.ID, a.Backend, a.Created.Format("2006-01-02 15:04"), task)
			found = true
		}
	}
	if !found {
		fmt.Println("No matching artifacts.")
	}
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

func defaultConversationForTransport(cfg store.Config, transport string) (chat.ConversationRef, error) {
	switch strings.TrimSpace(transport) {
	case "telegram":
		if _, err := cfg.Telegram(); err != nil {
			return chat.ConversationRef{}, err
		}
		return botpkg.ConfigConversation(cfg), nil
	case "slack":
		conversation := slackDefaultConversation(cfg)
		if conversation.ChannelID == "" {
			return chat.ConversationRef{}, fmt.Errorf("slack schedule target requires channels.slack.channel_id")
		}
		return conversation, nil
	default:
		return chat.ConversationRef{}, fmt.Errorf("unsupported transport: %s", transport)
	}
}

func resolveScheduleConversation(cfg store.Config, requestedTransport, rawConversation string) (chat.ConversationRef, error) {
	rawConversation = strings.TrimSpace(rawConversation)
	if rawConversation != "" {
		conversation := chat.ParseConversationID(rawConversation)
		if conversation.Provider == "" || strings.TrimSpace(conversation.ChannelID) == "" {
			return chat.ConversationRef{}, fmt.Errorf("invalid conversation id: %s", rawConversation)
		}
		return conversation, nil
	}

	transport, err := chooseServeTransport(cfg, requestedTransport)
	if err != nil {
		return chat.ConversationRef{}, err
	}
	return defaultConversationForTransport(cfg, transport)
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
	transportFlag := fs.String("transport", "", "")
	conversationFlag := fs.String("conversation", "", "")

	if err := fs.Parse(args); err != nil {
		fatal("usage: moxie schedule add (--transport <telegram|slack>|--conversation <provider:channel[:thread]>) --action <send|dispatch> (--in <duration>|--at <time>|--cron <spec>) --text <text>")
	}
	if fs.NArg() > 0 {
		fatal("unexpected schedule add args: %s", strings.Join(fs.Args(), " "))
	}

	cfg, err := store.LoadConfig()
	if err != nil {
		fatal("%v", err)
	}
	conversation, err := resolveScheduleConversation(cfg, *transportFlag, *conversationFlag)
	if err != nil {
		fatal("schedule add requires an explicit target conversation: %v", err)
	}

	state := store.ReadConversationState(conversation.ID())
	input := scheduler.AddInput{
		Trigger:        mustScheduleTrigger(*in, *at, *cronSpec),
		Action:         scheduler.Action(strings.TrimSpace(*action)),
		In:             *in,
		At:             *at,
		Cron:           *cronSpec,
		Text:           *text,
		ConversationID: conversation.ID(),
		Backend:        state.Backend,
		Model:          state.Model,
		ThreadID:       state.ThreadID,
		CWD:            state.CWD,
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
	if sc.RunningJobID != "" {
		fmt.Fprintf(&buf, "Running job: %s\n", sc.RunningJobID)
	}
	if sc.Action == scheduler.ActionDispatch {
		if sc.ConversationID != "" {
			fmt.Fprintf(&buf, "Conversation: %s\n", sc.ConversationID)
		}
		fmt.Fprintf(&buf, "Backend: %s\n", sc.Backend)
		if sc.Model != "" {
			fmt.Fprintf(&buf, "Model: %s\n", sc.Model)
		}
		fmt.Fprintf(&buf, "Thread: %s\n", sc.ThreadID)
		if sc.CWD != "" {
			fmt.Fprintf(&buf, "CWD: %s\n", sc.CWD)
		}
	} else if sc.ConversationID != "" {
		fmt.Fprintf(&buf, "Conversation: %s\n", sc.ConversationID)
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

// --- Subagent ---

type subagentArgs struct {
	backend   string
	text      string
	budget    int
	cwd       string
	model     string
	parentJob string
}

func parseSubagentArgs() *subagentArgs {
	fs := flag.NewFlagSet("subagent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if len(os.Args) > 2 {
		arg := os.Args[2]
		if arg == "help" || arg == "--help" || arg == "-h" {
			subagentUsage()
			os.Exit(0)
		}
	}

	args := &subagentArgs{}
	fs.StringVar(&args.backend, "backend", "", "")
	fs.StringVar(&args.text, "text", "", "")
	fs.IntVar(&args.budget, "context-budget", 8192, "")
	fs.StringVar(&args.cwd, "cwd", "", "")
	fs.StringVar(&args.model, "model", "", "")
	fs.StringVar(&args.parentJob, "parent-job", "", "")

	if err := fs.Parse(os.Args[2:]); err != nil || args.backend == "" || args.text == "" {
		subagentUsage()
		os.Exit(1)
	}
	return args
}

func findParentJob(parentID string) *store.PendingJob {
	jobs := store.ListJobs()
	if parentID != "" {
		for _, j := range jobs {
			j := j
			if j.ID == parentID {
				return &j
			}
		}
		fatal("parent job not found: %s", parentID)
	}
	var parent *store.PendingJob
	for _, j := range jobs {
		j := j
		if j.Status == "running" && j.Source != "subagent" {
			if parent != nil {
				fatal("multiple active dispatches — use --parent-job <id> to disambiguate")
			}
			parent = &j
		}
	}
	if parent == nil {
		fatal("no active dispatch found — moxie subagent can only be called from within a running dispatch")
	}
	return parent
}

func buildSubagentPrompt(text string, parent *store.PendingJob, budget int) string {
	if parent.State.ThreadID == "" {
		return text
	}
	thread, err := oneagent.LoadThread(parent.State.ThreadID)
	if err != nil || thread == nil || len(thread.Turns) == 0 {
		return text
	}
	ctx, _ := thread.CompileContext(budget)
	ctx = stripSubagentLines(ctx)
	if ctx == "" {
		return text
	}
	return "Context from parent conversation:\n" + ctx + "\n\nTask:\n" + text
}

func resolveReplyConversation(parent *store.PendingJob) string {
	if parent.ReplyConversation != "" {
		return parent.ReplyConversation
	}
	replyConversation := parent.ConversationID
	if chat.ParseConversationID(parent.ConversationID).Provider == chat.ProviderSlack {
		if ref := slackpkg.ReadReplyConversation(parent.ID); ref.Provider == chat.ProviderSlack && ref.ChannelID != "" {
			replyConversation = ref.ID()
		}
	}
	return replyConversation
}

func cmdSubagent() {
	args := parseSubagentArgs()

	cfg, err := store.LoadConfig()
	if err != nil {
		fatal("load config: %v", err)
	}
	maxDepth := cfg.MaxSubagentDepth()

	parent := findParentJob(args.parentJob)

	depth := parent.Depth + 1
	if depth > maxDepth {
		fatal("subagent depth limit reached (%d/%d) — handle this task directly", depth, maxDepth)
	}

	delegationCtx := parent.Prompt
	if len(delegationCtx) > 200 {
		delegationCtx = delegationCtx[:200]
	}

	cwd := parent.CWD
	if args.cwd != "" {
		resolved, err := resolveDir(args.cwd)
		if err != nil {
			fatal("invalid --cwd: %v", err)
		}
		cwd = resolved
	}

	job := store.PendingJob{
		ID:                store.NewJobID(),
		ConversationID:    parent.ConversationID,
		ReplyConversation: resolveReplyConversation(parent),
		Source:            "subagent",
		Prompt:            buildSubagentPrompt(args.text, parent, args.budget),
		CWD:               cwd,
		ParentJobID:       parent.ID,
		DelegatedTask:     args.text,
		DelegationContext: delegationCtx,
		Depth:             depth,
		SynthesisState:    parent.State,
		State: store.State{
			Backend:  args.backend,
			Model:    args.model,
			ThreadID: fmt.Sprintf("sub-%s-%d", parent.State.ThreadID, time.Now().UnixNano()),
		},
	}
	store.WriteJob(job)

	fmt.Printf("subagent dispatched: %s\nbackend: %s\ndepth: %d/%d\ntask: %s\n", job.ID, args.backend, depth, maxDepth, args.text)
}

// --- Serve loop ---

type serveFlags struct {
	cwd       string
	transport string
}

func parseServeTransportAndCWD() serveFlags {
	flags := serveFlags{}
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--cwd" && i+1 < len(os.Args) {
			flags.cwd = os.Args[i+1]
			i++
			continue
		}
		if os.Args[i] == "--transport" && i+1 < len(os.Args) {
			flags.transport = strings.TrimSpace(os.Args[i+1])
			i++
		}
	}
	return flags
}

func parseTransportFlag(startIdx int) string {
	for i := startIdx; i < len(os.Args); i++ {
		if os.Args[i] == "--transport" && i+1 < len(os.Args) {
			return strings.TrimSpace(os.Args[i+1])
		}
	}
	return ""
}

func joinArgsExcludingTransport(startIdx int) string {
	args := make([]string, 0, len(os.Args)-startIdx)
	for i := startIdx; i < len(os.Args); i++ {
		if os.Args[i] == "--transport" && i+1 < len(os.Args) {
			i++
			continue
		}
		args = append(args, os.Args[i])
	}
	return strings.Join(args, " ")
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
		st.ThreadID = "chat"
	}
	return st
}

func scheduledConversationID(sc scheduler.Schedule, fallbackConversationID string) string {
	if trimmed := strings.TrimSpace(sc.ConversationID); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(fallbackConversationID)
}

func runDueSchedulesTelegram(bot *tb.Bot, client *oneagent.Client, schedules *scheduler.Store, fallbackConversationID string) {
	due, err := schedules.Due(time.Now())
	if err != nil {
		log.Printf("schedule due check failed: %v", err)
		return
	}
	for _, sc := range due {
		conversationID := scheduledConversationID(sc, fallbackConversationID)
		if chat.ParseConversationID(conversationID).Provider != chat.ProviderTelegram {
			continue
		}
		job := botpkg.PendingJob{
			ID:             store.NewJobID(),
			ScheduleID:     sc.ID,
			ConversationID: conversationID,
			Source:         "schedule",
			CWD:            sc.CWD,
			State:          scheduleState(sc),
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
		if _, err := schedules.AttachJob(sc.ID, job.ID); err != nil {
			log.Printf("schedule attach failed for %s: %v", sc.ID, err)
			store.RemoveJob(job.ID)
			continue
		}

		log.Printf("running schedule %s via job %s", sc.ID, job.ID)
		botpkg.ProcessJob(job, bot, client, schedules)
		if dispatch.IsShuttingDown() {
			return
		}
	}
}

func startTickerLoop(ctx context.Context, interval time.Duration, fn func()) {
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		fn()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if dispatch.IsShuttingDown() {
					return
				}
				fn()
			}
		}
	}()
}

func startScheduleLoopTelegram(ctx context.Context, bot *tb.Bot, client *oneagent.Client, schedules *scheduler.Store, conversationID string) {
	startTickerLoop(ctx, 30*time.Second, func() {
		runDueSchedulesTelegram(bot, client, schedules, conversationID)
	})
}

func startDeliveryRetryLoopTelegram(ctx context.Context, bot *tb.Bot, client *oneagent.Client, schedules *scheduler.Store) {
	startTickerLoop(ctx, 15*time.Second, func() {
		botpkg.RetryDeliverableJobs(bot, client, schedules)
	})
}

func runDueSchedulesSlack(api slackpkg.Messenger, client *oneagent.Client, schedules *scheduler.Store, fallbackConversationID string) {
	due, err := schedules.Due(time.Now())
	if err != nil {
		log.Printf("schedule due check failed: %v", err)
		return
	}
	for _, sc := range due {
		conversationID := scheduledConversationID(sc, fallbackConversationID)
		if chat.ParseConversationID(conversationID).Provider != chat.ProviderSlack {
			continue
		}
		job := store.PendingJob{
			ID:             store.NewJobID(),
			ScheduleID:     sc.ID,
			ConversationID: conversationID,
			Source:         "schedule",
			CWD:            sc.CWD,
			State:          scheduleState(sc),
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
		if _, err := schedules.AttachJob(sc.ID, job.ID); err != nil {
			log.Printf("schedule attach failed for %s: %v", sc.ID, err)
			store.RemoveJob(job.ID)
			continue
		}

		log.Printf("running schedule %s via job %s", sc.ID, job.ID)
		slackpkg.ProcessJob(job, api, client, schedules)
		if dispatch.IsShuttingDown() {
			return
		}
	}
}

func startScheduleLoopSlack(ctx context.Context, api slackpkg.Messenger, client *oneagent.Client, schedules *scheduler.Store, conversationID string) {
	startTickerLoop(ctx, 30*time.Second, func() {
		runDueSchedulesSlack(api, client, schedules, conversationID)
	})
}

func startDeliveryRetryLoopSlack(ctx context.Context, api slackpkg.Messenger, client *oneagent.Client, schedules *scheduler.Store) {
	startTickerLoop(ctx, 15*time.Second, func() {
		slackpkg.RetryDeliverableJobs(api, client, schedules)
	})
}

// --- Subagent job watcher ---

// stripSubagentLines removes lines mentioning "moxie subagent" from text.
func stripSubagentLines(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "moxie subagent") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

type subagentTransports struct {
	telegramBot    *tb.Bot
	telegramClient *oneagent.Client
	slackAPI       slackpkg.Messenger
	slackClient    *oneagent.Client
	schedules      *scheduler.Store
	maxDepth       int
}

func startSubagentWatcher(ctx context.Context, cfg store.Config, backends map[string]oneagent.Backend, schedules *scheduler.Store) {
	st := subagentTransports{
		schedules: schedules,
		maxDepth:  cfg.MaxSubagentDepth(),
	}

	if _, err := cfg.Telegram(); err == nil {
		if bot, err := botpkg.NewBot(cfg); err == nil {
			st.telegramBot = bot
			st.telegramClient = newTelegramClient(backends)
		}
	}
	if _, err := cfg.Slack(); err == nil {
		if adapter, err := slackpkg.New(&cfg, "", nil, nil); err == nil {
			st.slackAPI = adapter.API()
			st.slackClient = newSlackClient(backends)
		}
	}

	startTickerLoop(ctx, 3*time.Second, func() {
		runSubagentJobs(st)
	})
}

func runSubagentJobs(st subagentTransports) {
	jobs := store.ListJobs()
	for _, job := range jobs {
		if job.Source != "subagent" || (job.Status != "" && job.Status != "running" && job.Status != "ready") {
			continue
		}
		job := job
		log.Printf("dispatching subagent job %s (backend=%s depth=%d/%d)", job.ID, job.State.Backend, job.Depth, st.maxDepth)

		provider := chat.ParseConversationID(job.ConversationID).Provider
		switch provider {
		case chat.ProviderTelegram:
			if st.telegramBot == nil || st.telegramClient == nil {
				log.Printf("subagent job %s targets telegram but no telegram transport", job.ID)
				continue
			}
			dispatch.ProcessJob(&job, st.telegramClient, st.schedules, subagentCallbacks(
				job, st.telegramClient, st.schedules,
				func(synthJob store.PendingJob) error {
					return botpkg.DeliverJobResult(st.telegramBot, &synthJob)
				},
			))
		case chat.ProviderSlack:
			if st.slackAPI == nil || st.slackClient == nil {
				log.Printf("subagent job %s targets slack but no slack transport", job.ID)
				continue
			}
			dispatch.ProcessJob(&job, st.slackClient, st.schedules, subagentCallbacks(
				job, st.slackClient, st.schedules,
				func(synthJob store.PendingJob) error {
					return slackpkg.DeliverJobResult(st.slackAPI, &synthJob)
				},
			))
		default:
			log.Printf("subagent job %s has unknown provider %s", job.ID, provider)
		}

		if dispatch.IsShuttingDown() {
			return
		}
	}
}

func subagentCallbacks(job store.PendingJob, synthClient *oneagent.Client, schedules *scheduler.Store, deliver func(store.PendingJob) error) dispatch.Callbacks {
	return dispatch.Callbacks{
		OnActivity:    func(string) {},
		OnStatusClear: func() {},
		OnDone:        func() {},
		OnResult: func(result string) error {
			return dispatchSynthesis(job, result, synthClient, schedules, deliver)
		},
	}
}

// dispatchSynthesis creates a new dispatch on the parent conversation's thread
// so the main agent can synthesize the subagent result with full context.
func dispatchSynthesis(subJob store.PendingJob, result string, client *oneagent.Client, schedules *scheduler.Store, deliver func(store.PendingJob) error) error {
	convState := subJob.SynthesisState
	if convState == (store.State{}) {
		convState = store.ReadConversationState(subJob.ConversationID)
	}
	prompt := buildSynthesisPrompt(subJob.DelegationContext, subJob.DelegatedTask, subJob.State.Backend, result)
	replyConversation := subJob.ReplyConversation
	if replyConversation == "" {
		replyConversation = subJob.ConversationID
	}

	synthJob := store.PendingJob{
		ID:                store.NewJobID(),
		ConversationID:    subJob.ConversationID,
		ReplyConversation: replyConversation,
		Source:            "subagent-synthesis",
		Prompt:            prompt,
		CWD:               subJob.CWD,
		Depth:             subJob.Depth,
		Status:            "running",
		State: store.State{
			Backend:  convState.Backend,
			Model:    convState.Model,
			ThreadID: convState.ThreadID,
		},
	}
	store.WriteJob(synthJob)
	log.Printf("dispatching synthesis job %s for subagent %s on thread %s", synthJob.ID, subJob.ID, convState.ThreadID)

	dispatch.ProcessJob(&synthJob, client, schedules, dispatch.Callbacks{
		OnActivity:    func(string) {},
		OnStatusClear: func() {},
		OnDone:        func() {},
		OnResult: func(synthResult string) error {
			synthJob.Result = synthResult
			return deliver(synthJob)
		},
	})
	return nil
}

func buildSynthesisPrompt(delegationCtx, task, backend, result string) string {
	if task == "" {
		task = "(unspecified)"
	}
	var b strings.Builder
	b.WriteString("A background agent you delegated to has completed.\n")
	if delegationCtx != "" {
		b.WriteString("Original context: ")
		b.WriteString(delegationCtx)
		b.WriteString("\n")
	}
	b.WriteString("Task: ")
	b.WriteString(task)
	b.WriteString("\nBackend: ")
	b.WriteString(backend)
	b.WriteString("\n\nResult:\n")
	b.WriteString(result)
	b.WriteString("\n\nSynthesize this result for the user. Reference what they were working on when you delegated this task.")
	return b.String()
}

func chooseServeTransport(cfg store.Config, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		switch requested {
		case "telegram":
			if _, err := cfg.Telegram(); err != nil {
				return "", err
			}
			return requested, nil
		case "slack":
			if _, err := cfg.Slack(); err != nil {
				return "", err
			}
			return requested, nil
		default:
			return "", fmt.Errorf("unknown transport: %s", requested)
		}
	}

	hasTelegram := false
	if _, err := cfg.Telegram(); err == nil {
		hasTelegram = true
	}
	hasSlack := false
	if _, err := cfg.Slack(); err == nil {
		hasSlack = true
	}

	switch {
	case hasTelegram && !hasSlack:
		return "telegram", nil
	case hasSlack && !hasTelegram:
		return "slack", nil
	case hasSlack && hasTelegram:
		return "", fmt.Errorf("multiple transports configured; use --transport telegram or --transport slack")
	default:
		return "", fmt.Errorf("no valid transport configured")
	}
}

func chooseServeTransports(cfg store.Config, requested string) ([]string, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		transport, err := chooseServeTransport(cfg, requested)
		if err != nil {
			return nil, err
		}
		return []string{transport}, nil
	}

	transports := make([]string, 0, 2)
	if _, err := cfg.Telegram(); err == nil {
		transports = append(transports, "telegram")
	}
	if _, err := cfg.Slack(); err == nil {
		transports = append(transports, "slack")
	}
	if len(transports) == 0 {
		return nil, fmt.Errorf("no valid transport configured")
	}
	return transports, nil
}

func cloneBackends(backends map[string]oneagent.Backend) map[string]oneagent.Backend {
	cloned := make(map[string]oneagent.Backend, len(backends))
	for name, backend := range backends {
		cloned[name] = backend
	}
	return cloned
}

func newTelegramClient(backends map[string]oneagent.Backend) *oneagent.Client {
	cloned := cloneBackends(backends)
	botpkg.ApplySystemPrompt(cloned)
	return &oneagent.Client{Backends: cloned}
}

func newSlackClient(backends map[string]oneagent.Backend) *oneagent.Client {
	cloned := cloneBackends(backends)
	slackpkg.ApplySystemPrompt(cloned)
	return &oneagent.Client{Backends: cloned}
}

type serveTransportRuntime struct {
	name string
	run  func(context.Context) error
}

type serveTransportResult struct {
	name string
	err  error
}

type serveSignalAction int

const (
	serveSignalNone serveSignalAction = iota
	serveSignalStop
	serveSignalReload
)

func runServeSupervisor(ctx context.Context, transports []serveTransportRuntime) error {
	if len(transports) == 0 {
		return fmt.Errorf("no serve transports configured")
	}

	results := make(chan serveTransportResult, len(transports))
	for _, transport := range transports {
		transport := transport
		go func() {
			log.Printf("starting %s transport", transport.name)
			results <- serveTransportResult{name: transport.name, err: transport.run(ctx)}
		}()
	}

	failures := 0
	remaining := len(transports)
	for remaining > 0 {
		result := <-results
		remaining--
		switch {
		case result.err != nil && ctx.Err() == nil && !dispatch.IsShuttingDown():
			failures++
			log.Printf("%s transport exited with error: %v", result.name, result.err)
		case result.err != nil:
			log.Printf("%s transport stopped: %v", result.name, result.err)
		default:
			log.Printf("%s transport stopped", result.name)
		}
	}

	if failures == len(transports) {
		return fmt.Errorf("all configured transports failed")
	}
	return nil
}

func cmdServe() {
	flags := parseServeTransportAndCWD()
	defaultCWD := flags.cwd
	if defaultCWD != "" {
		var err error
		defaultCWD, err = resolveDir(defaultCWD)
		if err != nil {
			fatal("invalid --cwd: %v", err)
		}
	}

	for {
		action, err := runServeOnce(flags.transport, defaultCWD)
		if action == serveSignalReload {
			if err != nil {
				log.Printf("reload completed after transport stop: %v", err)
			}
			log.Println("reload requested; reloading config and restarting transports")
			continue
		}
		if err != nil {
			fatal("%v", err)
		}
		return
	}
}

func runServeOnce(requestedTransport, defaultCWD string) (serveSignalAction, error) {
	cfg, err := store.LoadConfig()
	if err != nil {
		return serveSignalNone, err
	}

	backends, err := loadServeBackends()
	if err != nil {
		return serveSignalNone, fmt.Errorf("no backends: %w", err)
	}

	schedules := newScheduleStore()
	if err := schedules.Repair(store.JobExists); err != nil {
		log.Printf("schedule repair failed: %v", err)
	}
	transports, err := chooseServeTransports(cfg, requestedTransport)
	if err != nil {
		return serveSignalNone, err
	}

	dispatch.SetShuttingDown(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	actionCh, stopSignals := installServeSignalHandler(cancel)
	defer stopSignals()

	runtimes := make([]serveTransportRuntime, 0, len(transports))
	for _, transport := range transports {
		switch transport {
		case "telegram":
			client := newTelegramClient(backends)
			runtimes = append(runtimes, serveTransportRuntime{
				name: "telegram",
				run: func(ctx context.Context) error {
					return runTelegramTransport(ctx, cfg, defaultCWD, client, schedules)
				},
			})
		case "slack":
			client := newSlackClient(backends)
			runtimes = append(runtimes, serveTransportRuntime{
				name: "slack",
				run: func(ctx context.Context) error {
					return runSlackTransport(ctx, cfg, defaultCWD, client, schedules)
				},
			})
		default:
			fatal("unsupported transport: %s", transport)
		}
	}

	startSubagentWatcher(ctx, cfg, backends, schedules)

	err = runServeSupervisor(ctx, runtimes)
	return serveSignalActionFromChannel(actionCh), err
}

func loadServeBackends() (map[string]oneagent.Backend, error) {
	return oneagent.LoadBackendsWithOptions(oneagent.LoadOptions{
		IncludeEmbedded: true,
		OverridePath:    store.ConfigFile("backends.json"),
	})
}

func installServeSignalHandler(cancel func()) (<-chan serveSignalAction, func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	actions := make(chan serveSignalAction, 1)
	stop := make(chan struct{})
	go func() {
		defer close(actions)
		select {
		case s := <-sig:
			dispatch.SetShuttingDown(true)
			if cancel != nil {
				cancel()
			}
			switch s {
			case syscall.SIGHUP:
				log.Println("reload requested; draining current work")
				actions <- serveSignalReload
			default:
				log.Println("shutdown requested; draining current work")
				actions <- serveSignalStop
			}
		case <-stop:
			return
		}
	}()
	return actions, func() {
		signal.Stop(sig)
		close(stop)
	}
}

func serveSignalActionFromChannel(actionCh <-chan serveSignalAction) serveSignalAction {
	select {
	case action, ok := <-actionCh:
		if !ok {
			return serveSignalNone
		}
		return action
	default:
		return serveSignalNone
	}
}

func runTelegramTransport(ctx context.Context, cfg store.Config, defaultCWD string, client *oneagent.Client, schedules *scheduler.Store) error {
	bot, err := botpkg.NewBot(cfg)
	if err != nil {
		return fmt.Errorf("bot init failed: %w", err)
	}

	botpkg.RecoverPendingJobs(bot, client, schedules)
	if botpkg.ReadCursor() == 0 {
		botpkg.SeedCursor(bot, getUpdates)
	}
	botpkg.RegisterCommands(bot)
	conversation := botpkg.ConfigConversation(cfg)
	startScheduleLoopTelegram(ctx, bot, client, schedules, conversation.ID())
	startDeliveryRetryLoopTelegram(ctx, bot, client, schedules)

	cursor := botpkg.ReadCursor()
	st := store.ReadConversationState(conversation.ID())
	log.Printf("telegram transport ready — conversation=%s backend=%s thread=%s cwd=%s", conversation.ID(), st.Backend, st.ThreadID, defaultCWD)

	offset := func() int {
		if cursor > 0 {
			return cursor + 1
		}
		return 0
	}

	for ctx.Err() == nil && !dispatch.IsShuttingDown() {
		for _, u := range getUpdates(bot, offset(), 30) {
			if ctx.Err() != nil || dispatch.IsShuttingDown() {
				return nil
			}
			st = store.ReadConversationState(conversation.ID())
			botpkg.HandleUpdate(u, bot, &cfg, defaultCWD, st, client)
			cursor = u.ID
			botpkg.WriteCursor(cursor)
			if ctx.Err() != nil || dispatch.IsShuttingDown() {
				return nil
			}
		}
	}
	return nil
}

func slackDefaultConversation(cfg store.Config) chat.ConversationRef {
	ch, err := cfg.Slack()
	if err != nil {
		return chat.ConversationRef{Provider: chat.ProviderSlack}
	}
	return chat.ConversationRef{
		Provider:  chat.ProviderSlack,
		ChannelID: ch.ChannelID,
	}
}

func runSlackTransport(ctx context.Context, cfg store.Config, defaultCWD string, client *oneagent.Client, schedules *scheduler.Store) error {
	adapter, err := slackpkg.New(&cfg, defaultCWD, client, schedules)
	if err != nil {
		return fmt.Errorf("slack init failed: %w", err)
	}

	slackpkg.RecoverPendingJobs(adapter.API(), client, schedules)
	defaultConversation := slackDefaultConversation(cfg)
	if defaultConversation.ChannelID != "" {
		startScheduleLoopSlack(ctx, adapter.API(), client, schedules, defaultConversation.ID())
	}
	startDeliveryRetryLoopSlack(ctx, adapter.API(), client, schedules)

	st := store.ReadConversationState(defaultConversation.ID())
	log.Printf("slack transport ready — conversation=%s backend=%s thread=%s cwd=%s", defaultConversation.ID(), st.Backend, st.ThreadID, defaultCWD)

	if err := adapter.Run(ctx); err != nil && ctx.Err() == nil && !dispatch.IsShuttingDown() {
		return fmt.Errorf("slack serve failed: %w", err)
	}
	return nil
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
