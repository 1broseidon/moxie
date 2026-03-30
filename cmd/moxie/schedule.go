package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"

	botpkg "github.com/1broseidon/moxie/internal/bot"
	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/scheduler"
	slackpkg "github.com/1broseidon/moxie/internal/slack"
	"github.com/1broseidon/moxie/internal/store"
	webexpkg "github.com/1broseidon/moxie/internal/webex"
	"github.com/1broseidon/oneagent"
	tb "gopkg.in/telebot.v4"
)

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
	case "fire":
		cmdScheduleFire(scheduleStore, os.Args[3:])
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

Internal/operator:
  moxie schedule fire <id>

Flags for add:
  --transport <telegram|slack|webex>          Use the configured default conversation for one transport
  --conversation <provider:channel[:thread]>  Target a specific provider conversation directly
  --action <send|dispatch|exec>                Send a fixed message, dispatch agent work, or run a command
  --text <text>                               Message text or dispatch task
  --in <duration>                             Relative one-shot schedule like 5m, 2h, or 1d2h30m
  --at <time>                                 Exact one-shot timestamp (RFC3339, YYYY-MM-DDTHH:MM, or YYYY-MM-DD HH:MM)
  --every <duration>                          Recurring interval schedule like 15m, 2h, or 24h
  --cron <spec>                               Recurring portable cron schedule
  --backend <name>                            Override captured backend for dispatch schedules
  --model <name>                              Override captured model for dispatch schedules
  --thread <id>                               Override captured thread for dispatch schedules
  --cwd <dir>                                 Override captured working directory for dispatch schedules

Notes:
  Use exactly one of --in, --at, --every, or --cron
  Use --conversation to target a specific provider conversation, or --transport to target the configured default conversation for one transport
  If only one transport is configured, --transport can be omitted
  Dispatch schedules capture backend/model/thread/cwd at creation time
  schedule fire is internal/operator plumbing used by native scheduler backends

When to use:
  Only when explicitly asked to create, inspect, modify, or delete schedules
  Prefer --in for one-shot relative times, --at for exact timestamps, --every for recurring interval schedules, and --cron for recurring calendar schedules
  Use action send for fixed messages, dispatch for agent work, or exec to run a command and send its output

Examples:
  moxie schedule add --transport telegram --action send --in 5m --text "Call John"
  moxie schedule add --transport telegram --action dispatch --every 30m --text "Check queue depth"
  moxie schedule add --conversation slack:C123:1710000000.100 --action send --at 2026-03-18T10:00:00-05:00 --text "Call John"
  moxie schedule add --transport slack --action dispatch --cron "0 1 * * *" --text "Run a security scan"
  moxie schedule add --transport telegram --action exec --cron "7 * * * *" --text "/path/to/check-email.sh"
  moxie schedule list
  moxie schedule show sch-123
  moxie schedule rm sch-123`)
}

func resolveScheduleTrigger(in, at, every, cronSpec string) (scheduler.Trigger, error) {
	count := 0
	trigger := scheduler.TriggerAt
	if strings.TrimSpace(in) != "" {
		count++
		trigger = scheduler.TriggerAt
	}
	if strings.TrimSpace(at) != "" {
		count++
		trigger = scheduler.TriggerAt
	}
	if strings.TrimSpace(every) != "" {
		count++
		trigger = scheduler.TriggerInterval
	}
	if strings.TrimSpace(cronSpec) != "" {
		count++
		trigger = scheduler.TriggerCalendar
	}
	if count == 0 {
		return "", fmt.Errorf("missing schedule trigger: use --in, --at, --every, or --cron")
	}
	if count > 1 {
		return "", fmt.Errorf("use exactly one of --in, --at, --every, or --cron")
	}
	return trigger, nil
}

func mustScheduleTrigger(in, at, every, cronSpec string) scheduler.Trigger {
	trigger, err := resolveScheduleTrigger(in, at, every, cronSpec)
	if err != nil {
		fatal("%v", err)
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
	if input.Action != scheduler.ActionSend && input.Action != scheduler.ActionExec {
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
	case "webex":
		conversation := webexDefaultConversation(cfg)
		if conversation.ChannelID == "" {
			return chat.ConversationRef{}, fmt.Errorf("webex schedule target requires channels.webex.channel_id (a 1:1 direct room ID)")
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
	every := fs.String("every", "", "")
	cronSpec := fs.String("cron", "", "")
	text := fs.String("text", "", "")
	cwdFlag := fs.String("cwd", "", "")
	backendFlag := fs.String("backend", "", "")
	modelFlag := fs.String("model", "", "")
	threadFlag := fs.String("thread", "", "")
	transportFlag := fs.String("transport", "", "")
	conversationFlag := fs.String("conversation", "", "")

	if err := fs.Parse(args); err != nil {
		fatal("usage: moxie schedule add (--transport <telegram|slack|webex>|--conversation <provider:channel[:thread]>) --action <send|dispatch|exec> (--in <duration>|--at <time>|--every <duration>|--cron <spec>) --text <text>")
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
		Trigger:        mustScheduleTrigger(*in, *at, *every, *cronSpec),
		Action:         scheduler.Action(strings.TrimSpace(*action)),
		In:             *in,
		At:             *at,
		Every:          *every,
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

	// Resource limits: cap schedules per conversation and inherit generation.
	input.MaxPerConv = cfg.MaxSchedulesPerConvLimit()
	if parent := findParentJobOptional(); parent != nil {
		input.Generation = parentScheduleGeneration(parent) + 1
		maxGen := cfg.MaxScheduleGenerationLimit()
		if input.Generation > maxGen {
			fatal("schedule generation limit reached (%d/%d) — a scheduled dispatch created this agent, which is trying to create another schedule. This recursive pattern is capped.", input.Generation, maxGen)
		}
	}

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

func cmdScheduleFire(scheduleStore *scheduler.Store, args []string) {
	if len(args) != 1 {
		fatal("usage: moxie schedule fire <id>")
	}
	id := strings.TrimSpace(args[0])
	sc, err := loadScheduleForFire(scheduleStore, id)
	if err != nil {
		if os.IsNotExist(err) {
			fatal("unknown schedule: %s", id)
		}
		fatal("schedule fire failed: %v", err)
	}
	jobID, alreadyRunning, err := fireScheduleExecution(scheduleStore, sc)
	if err != nil {
		fatal("schedule fire failed: %v", err)
	}
	if alreadyRunning {
		fmt.Printf("schedule %s already running via job %s\n", id, jobID)
		return
	}
	fmt.Printf("fired %s via job %s\n", id, jobID)
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

// --- Schedule display helpers ---

func formatScheduleHeadline(sc scheduler.Schedule) string {
	switch sc.Spec.Trigger {
	case scheduler.TriggerAt:
		return fmt.Sprintf("%s at %s", sc.Action, formatScheduleTime(sc.NextRun))
	case scheduler.TriggerInterval:
		return fmt.Sprintf("%s every %s next %s", sc.Action, formatScheduleInterval(sc.Spec.Interval), formatScheduleTime(sc.NextRun))
	case scheduler.TriggerCalendar:
		return fmt.Sprintf("%s %s %s next %s", sc.Action, scheduleCalendarLabel(sc), scheduleCalendarDisplay(sc), formatScheduleTime(sc.NextRun))
	default:
		return fmt.Sprintf("%s next %s", sc.Action, formatScheduleTime(sc.NextRun))
	}
}

func renderSchedule(sc scheduler.Schedule) string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "ID: %s\n", sc.ID)
	fmt.Fprintf(&buf, "Action: %s\n", sc.Action)
	switch sc.Spec.Trigger {
	case scheduler.TriggerAt:
		fmt.Fprintf(&buf, "Trigger: at %s\n", formatScheduleTime(sc.Spec.At))
	case scheduler.TriggerInterval:
		fmt.Fprintf(&buf, "Trigger: every %s\n", formatScheduleInterval(sc.Spec.Interval))
	case scheduler.TriggerCalendar:
		fmt.Fprintf(&buf, "Trigger: %s %s\n", scheduleCalendarLabel(sc), scheduleCalendarDisplay(sc))
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
	if sc.Sync.ManagedBy != "" {
		fmt.Fprintf(&buf, "Managed by: %s\n", sc.Sync.ManagedBy)
	}
	if sc.Sync.State != "" {
		fmt.Fprintf(&buf, "Sync state: %s\n", sc.Sync.State)
	}
	if sc.Sync.Error != "" {
		fmt.Fprintf(&buf, "Sync error: %s\n", sc.Sync.Error)
	}
	fmt.Fprintf(&buf, "Text: %s\n", sc.Text)
	return strings.TrimSpace(buf.String())
}

func formatScheduleInterval(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return raw
	}
	return formatCompactDuration(d)
}

func formatCompactDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	sign := ""
	if d < 0 {
		sign = "-"
		d = -d
	}
	parts := make([]string, 0, 3)
	if hours := d / time.Hour; hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
		d -= hours * time.Hour
	}
	if minutes := d / time.Minute; minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
		d -= minutes * time.Minute
	}
	if seconds := d / time.Second; seconds > 0 {
		parts = append(parts, fmt.Sprintf("%ds", seconds))
	}
	if len(parts) == 0 {
		return sign + d.String()
	}
	return sign + strings.Join(parts, "")
}

func scheduleCalendarLabel(sc scheduler.Schedule) string {
	if sc.Spec.Calendar != nil && strings.TrimSpace(sc.Spec.Calendar.Cron) != "" {
		return "cron"
	}
	return "calendar"
}

func scheduleCalendarDisplay(sc scheduler.Schedule) string {
	if sc.Spec.Calendar == nil {
		return "(none)"
	}
	if display := strings.TrimSpace(sc.Spec.Calendar.DisplaySpec()); display != "" {
		return display
	}
	return "(none)"
}

func formatScheduleTime(t time.Time) string {
	if t.IsZero() {
		return "(none)"
	}
	return t.In(time.Local).Format("2006-01-02 15:04 MST")
}

// --- Schedule execution (serve loop) ---

func scheduleState(sc scheduler.Schedule) store.State {
	st := store.State{
		Backend:  sc.Backend,
		Model:    sc.Model,
		ThreadID: sc.ThreadID,
		CWD:      sc.CWD,
	}
	if st.Backend == "" {
		st.Backend = store.DefaultBackend()
	}
	if st.ThreadID == "" {
		st.ThreadID = "chat"
	}
	return st
}

type scheduleJobExecutor func(store.PendingJob) error

var (
	prepareTelegramScheduleFire = func(cfg store.Config, schedules *scheduler.Store, needsClient bool) (scheduleJobExecutor, error) {
		bot, err := botpkg.NewBot(cfg)
		if err != nil {
			return nil, err
		}
		var client *oneagent.Client
		if needsClient {
			backends, err := loadServeBackends()
			if err != nil {
				return nil, fmt.Errorf("no backends: %w", err)
			}
			client = newTelegramClient(backends)
		}
		return func(job store.PendingJob) error {
			botpkg.ProcessJob(job, bot, client, schedules)
			return nil
		}, nil
	}
	prepareSlackScheduleFire = func(cfg store.Config, schedules *scheduler.Store, needsClient bool) (scheduleJobExecutor, error) {
		var client *oneagent.Client
		if needsClient {
			backends, err := loadServeBackends()
			if err != nil {
				return nil, fmt.Errorf("no backends: %w", err)
			}
			client = newSlackClient(backends)
		}
		adapter, err := slackpkg.New(&cfg, "", client, schedules)
		if err != nil {
			return nil, err
		}
		return func(job store.PendingJob) error {
			slackpkg.ProcessJob(job, adapter.API(), client, schedules)
			return nil
		}, nil
	}
	prepareWebexScheduleFire = func(cfg store.Config, schedules *scheduler.Store, needsClient bool) (scheduleJobExecutor, error) {
		var client *oneagent.Client
		if needsClient {
			backends, err := loadServeBackends()
			if err != nil {
				return nil, fmt.Errorf("no backends: %w", err)
			}
			client = newWebexClient(backends)
		}
		adapter, err := webexpkg.New(&cfg, "", client, schedules)
		if err != nil {
			return nil, err
		}
		return func(job store.PendingJob) error {
			webexpkg.ProcessJob(job, adapter.API(), client, schedules)
			return nil
		}, nil
	}
)

func scheduledConversationID(sc scheduler.Schedule, fallbackConversationID string) string {
	if trimmed := strings.TrimSpace(sc.ConversationID); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(fallbackConversationID)
}

func loadScheduleForFire(scheduleStore *scheduler.Store, id string) (scheduler.Schedule, error) {
	if err := scheduleStore.Repair(store.JobExists); err != nil {
		return scheduler.Schedule{}, err
	}
	return scheduleStore.Get(strings.TrimSpace(id))
}

func runningScheduleJobID(sc scheduler.Schedule) (string, bool) {
	jobID := strings.TrimSpace(sc.RunningJobID)
	return jobID, jobID != "" && store.JobExists(jobID)
}

func buildScheduledJob(sc scheduler.Schedule, fallbackConversationID string) (store.PendingJob, error) {
	conversationID := scheduledConversationID(sc, fallbackConversationID)
	conversation := chat.ParseConversationID(conversationID)
	if conversation.Provider == "" || strings.TrimSpace(conversation.ChannelID) == "" {
		return store.PendingJob{}, fmt.Errorf("schedule %s has no valid conversation", sc.ID)
	}

	job := store.PendingJob{
		ID:                 store.NewJobID(),
		ScheduleID:         sc.ID,
		ConversationID:     conversationID,
		Source:             "schedule",
		CWD:                sc.CWD,
		ScheduleGeneration: sc.Generation,
		State:              scheduleState(sc),
	}
	switch sc.Action {
	case scheduler.ActionSend:
		job.Status = "ready"
		job.Result = sc.Text
	case scheduler.ActionDispatch:
		job.Prompt = sc.Text
	case scheduler.ActionExec:
		job.Prompt = sc.Text
		job.Source = "exec"
	default:
		return store.PendingJob{}, fmt.Errorf("unknown schedule action %q", sc.Action)
	}
	return job, nil
}

func claimScheduledJob(schedules *scheduler.Store, scheduleID string, job store.PendingJob) (string, bool, error) {
	store.WriteJob(job)
	if _, err := schedules.AttachJob(scheduleID, job.ID); err != nil {
		store.RemoveJob(job.ID)
		current, getErr := schedules.Get(scheduleID)
		if getErr == nil {
			if runningJobID, ok := runningScheduleJobID(current); ok {
				return runningJobID, true, nil
			}
		}
		return "", false, err
	}
	return job.ID, false, nil
}

func fireScheduleExecution(schedules *scheduler.Store, sc scheduler.Schedule) (string, bool, error) {
	if runningJobID, ok := runningScheduleJobID(sc); ok {
		return runningJobID, true, nil
	}

	cfg, err := store.LoadConfig()
	if err != nil {
		return "", false, err
	}
	job, err := buildScheduledJob(sc, "")
	if err != nil {
		return "", false, err
	}

	needsClient := sc.Action == scheduler.ActionDispatch
	conversation := chat.ParseConversationID(job.ConversationID)
	var execute scheduleJobExecutor
	switch conversation.Provider {
	case chat.ProviderTelegram:
		execute, err = prepareTelegramScheduleFire(cfg, schedules, needsClient)
	case chat.ProviderSlack:
		execute, err = prepareSlackScheduleFire(cfg, schedules, needsClient)
	case chat.ProviderWebex:
		execute, err = prepareWebexScheduleFire(cfg, schedules, needsClient)
	default:
		return "", false, fmt.Errorf("unsupported schedule provider: %s", conversation.Provider)
	}
	if err != nil {
		return "", false, err
	}

	jobID, alreadyRunning, err := claimScheduledJob(schedules, sc.ID, job)
	if err != nil {
		return "", false, err
	}
	if alreadyRunning {
		return jobID, true, nil
	}

	log.Printf("running schedule %s via job %s", sc.ID, job.ID)
	if err := execute(job); err != nil {
		return "", false, err
	}
	return jobID, false, nil
}

var (
	scheduleDueFailures atomic.Int32
	scheduleDueAlerted  atomic.Bool
)

const scheduleDueAlertThreshold = 5

func trackScheduleDueError(err error, conversationID string, sendAlert func(string)) {
	n := scheduleDueFailures.Add(1)
	log.Printf("schedule due check failed (%d consecutive): %v", n, err)
	if int(n) >= scheduleDueAlertThreshold && scheduleDueAlerted.CompareAndSwap(false, true) {
		msg := fmt.Sprintf("⚠️ Schedule system error: %d consecutive due-check failures. Schedules may not be firing.\n\nLast error: %v", n, err)
		if sendAlert != nil {
			sendAlert(msg)
		}
	}
}

func resetScheduleDueFailures() {
	scheduleDueFailures.Store(0)
	scheduleDueAlerted.Store(false)
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

// --- Per-transport schedule loops ---

func runDueSchedulesTelegram(bot *tb.Bot, client *oneagent.Client, schedules *scheduler.Store, fallbackConversationID string) {
	due, err := schedules.Due(time.Now())
	if err != nil {
		trackScheduleDueError(err, fallbackConversationID, func(msg string) {
			conversation := chat.ParseConversationID(fallbackConversationID)
			botpkg.SendChunked(bot, conversation, msg)
		})
		return
	}
	resetScheduleDueFailures()
	for _, sc := range due {
		job, err := buildScheduledJob(sc, fallbackConversationID)
		if err != nil {
			log.Printf("schedule job build failed for %s: %v", sc.ID, err)
			continue
		}
		if chat.ParseConversationID(job.ConversationID).Provider != chat.ProviderTelegram {
			continue
		}
		if _, alreadyRunning, err := claimScheduledJob(schedules, sc.ID, job); err != nil {
			log.Printf("schedule attach failed for %s: %v", sc.ID, err)
			continue
		} else if alreadyRunning {
			continue
		}

		log.Printf("running schedule %s via job %s", sc.ID, job.ID)
		botpkg.ProcessJob(job, bot, client, schedules)
		if dispatch.IsShuttingDown() {
			return
		}
	}
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
		trackScheduleDueError(err, fallbackConversationID, func(msg string) {
			conversation := chat.ParseConversationID(fallbackConversationID)
			slackpkg.SendPlainText(api, conversation, msg)
		})
		return
	}
	resetScheduleDueFailures()
	for _, sc := range due {
		job, err := buildScheduledJob(sc, fallbackConversationID)
		if err != nil {
			log.Printf("schedule job build failed for %s: %v", sc.ID, err)
			continue
		}
		if chat.ParseConversationID(job.ConversationID).Provider != chat.ProviderSlack {
			continue
		}
		if _, alreadyRunning, err := claimScheduledJob(schedules, sc.ID, job); err != nil {
			log.Printf("schedule attach failed for %s: %v", sc.ID, err)
			continue
		} else if alreadyRunning {
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

func runDueSchedulesWebex(api webexpkg.Messenger, client *oneagent.Client, schedules *scheduler.Store, fallbackConversationID string) {
	due, err := schedules.Due(time.Now())
	if err != nil {
		trackScheduleDueError(err, fallbackConversationID, func(msg string) {
			conversation := chat.ParseConversationID(fallbackConversationID)
			webexpkg.SendPlainText(api, conversation, msg)
		})
		return
	}
	resetScheduleDueFailures()
	for _, sc := range due {
		job, err := buildScheduledJob(sc, fallbackConversationID)
		if err != nil {
			log.Printf("schedule job build failed for %s: %v", sc.ID, err)
			continue
		}
		if chat.ParseConversationID(job.ConversationID).Provider != chat.ProviderWebex {
			continue
		}
		if _, alreadyRunning, err := claimScheduledJob(schedules, sc.ID, job); err != nil {
			log.Printf("schedule attach failed for %s: %v", sc.ID, err)
			continue
		} else if alreadyRunning {
			continue
		}

		log.Printf("running schedule %s via job %s", sc.ID, job.ID)
		webexpkg.ProcessJob(job, api, client, schedules)
		if dispatch.IsShuttingDown() {
			return
		}
	}
}

func startScheduleLoopWebex(ctx context.Context, api webexpkg.Messenger, client *oneagent.Client, schedules *scheduler.Store, conversationID string) {
	startTickerLoop(ctx, 30*time.Second, func() {
		runDueSchedulesWebex(api, client, schedules, conversationID)
	})
}

func startDeliveryRetryLoopWebex(ctx context.Context, api webexpkg.Messenger, client *oneagent.Client, schedules *scheduler.Store) {
	startTickerLoop(ctx, 15*time.Second, func() {
		webexpkg.RetryDeliverableJobs(api, client, schedules)
	})
}
