package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func subagentUsage() {
	fmt.Println(`moxie subagent — manage and delegate subagent work

Usage:
  moxie subagent --backend <name> --text <task>
  moxie subagent list [--all]
  moxie subagent show <job-id>
  moxie subagent cancel <job-id>

Subcommands:
  list              List active subagent jobs (use --all to include completed/canceled)
  show <job-id>     Show detailed status for a subagent job
  cancel <job-id>   Cancel a running subagent job

Flags for dispatch:
  --backend <name>            Required backend to run the delegated task
  --text <task>               Required self-contained task description
  --context-budget <n>        Context budget for compiled parent context (default 2048)
  --cwd <dir>                 Override working directory for the subagent
  --model <name>              Optional model override for the subagent backend
  --parent-job <id>           Explicit parent dispatch job to attach to

When to use:
  Only when delegating a distinct self-contained task to another backend
  Do not use it for simple questions or work you can handle directly
  Top-level subagent calls run asynchronously with context from the parent conversation
  Nested subagent calls block until the child finishes and print the child result to stdout
  Completed async runs are delivered back when ready

Examples:
  moxie subagent --backend codex --text "Audit the scheduler retry logic"
  moxie subagent list
  moxie subagent show job-1774236872176111993
  moxie subagent cancel job-1774236872176111993`)
}

// --- Subagent CLI ---

type subagentArgs struct {
	backend   string
	text      string
	budget    int
	cwd       string
	model     string
	parentJob string
}

var (
	subagentBlockingPollInterval = 50 * time.Millisecond
	subagentBlockingTimeout      = 10 * time.Minute
	skipSubagentPreflight        = false
)

func parseSubagentArgs() *subagentArgs {
	fs := flag.NewFlagSet("subagent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	args := &subagentArgs{}
	fs.StringVar(&args.backend, "backend", "", "")
	fs.StringVar(&args.text, "text", "", "")
	fs.IntVar(&args.budget, "context-budget", 2048, "")
	fs.StringVar(&args.cwd, "cwd", "", "")
	fs.StringVar(&args.model, "model", "", "")
	fs.StringVar(&args.parentJob, "parent-job", "", "")

	if err := fs.Parse(os.Args[2:]); err != nil || args.backend == "" || args.text == "" {
		subagentUsage()
		os.Exit(1)
	}
	return args
}

func currentJobID() string {
	return strings.TrimSpace(os.Getenv("MOXIE_JOB_ID"))
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
	if currentID := currentJobID(); currentID != "" {
		for _, j := range jobs {
			j := j
			if j.ID == currentID {
				return &j
			}
		}
		fatal("current job not found: %s", currentID)
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

// findParentJobOptional returns the current parent job if called from within
// an agent dispatch, or nil if not in a dispatch context. Unlike findParentJob
// it never fatals.
func findParentJobOptional() *store.PendingJob {
	currentID := currentJobID()
	if currentID == "" {
		return nil
	}
	for _, j := range store.ListJobs() {
		j := j
		if j.ID == currentID {
			return &j
		}
	}
	return nil
}

// parentScheduleGeneration extracts the schedule generation from a parent job.
// If the parent was itself created by a schedule, it carries that schedule's generation.
func parentScheduleGeneration(parent *store.PendingJob) int {
	if parent == nil {
		return 0
	}
	return parent.ScheduleGeneration
}

func shouldBlockOnSubagent(parent *store.PendingJob) bool {
	return parent != nil && parent.Depth > 0
}

func validateSubagentParent(parent *store.PendingJob) error {
	if parent == nil {
		return nil
	}
	if parent.Source == "subagent-synthesis" {
		return fmt.Errorf("subagent chaining from synthesis turns is disabled — inspect the result in the main thread or ask the user for another step")
	}
	return nil
}

func subagentBlockingResultPath(jobID string) string {
	return filepath.Join(store.JobsDir(), jobID+".result")
}

func writeBlockingSubagentResult(job store.PendingJob, result string) error {
	return dispatch.WriteBlockingSubagentResult(job, result)
}

func waitForBlockingSubagentResult(job store.PendingJob) (string, error) {
	deadline := time.Now().Add(subagentBlockingTimeout)
	for {
		if data, err := os.ReadFile(job.BlockingResultPath); err == nil {
			_ = os.Remove(job.BlockingResultPath)
			return string(data), nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", err
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for subagent %s", job.ID)
		}
		time.Sleep(subagentBlockingPollInterval)
	}
}

func buildSubagentPrompt(text string, parent *store.PendingJob, budget int) string {
	if parent.State.ThreadID == "" {
		return text
	}
	thread, err := oneagent.LoadThread(parent.State.ThreadID)
	if err != nil || thread == nil || len(thread.Turns) == 0 {
		return text
	}
	ctx := recentThreadTurnsContext(thread.Turns, 2, budget)
	ctx = stripSubagentLines(ctx)
	if strings.TrimSpace(ctx) == "" {
		return text
	}
	return ctx + "\n\nTask:\n" + text
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
	if len(os.Args) > 2 {
		switch os.Args[2] {
		case "list", "ls":
			cmdSubagentList(os.Args[3:])
			return
		case "show":
			cmdSubagentShow(os.Args[3:])
			return
		case "cancel":
			cmdSubagentCancel(os.Args[3:])
			return
		case "help", "--help", "-h":
			subagentUsage()
			return
		}
	}

	args := parseSubagentArgs()

	cfg, err := store.LoadConfig()
	if err != nil {
		fatal("load config: %v", err)
	}
	maxDepth := cfg.MaxSubagentDepth()

	parent := findParentJob(args.parentJob)
	if err := validateSubagentParent(parent); err != nil {
		log.Printf("blocked subagent chaining from synthesis turn (parent=%s)", parent.ID)
		fatal("%v", err)
	}
	blocking := shouldBlockOnSubagent(parent)

	depth := parent.Depth + 1
	if depth > maxDepth {
		fatal("subagent depth limit reached (%d/%d) — handle this task directly", depth, maxDepth)
	}

	// Width limit: cap concurrent subagent jobs per conversation.
	maxWidth := cfg.MaxPendingSubagentsLimit()
	pending := store.CountPendingSubagentJobs(parent.ConversationID)
	if pending >= maxWidth {
		fatal("subagent width limit reached (%d/%d pending for this conversation) — wait for existing subagents to finish or handle this task directly", pending, maxWidth)
	}

	// Rate limit: cap total jobs written per minute from subagent source.
	maxPerMin := cfg.MaxJobsPerMinuteLimit()
	recent := store.CountRecentJobs("subagent")
	if recent >= maxPerMin {
		fatal("subagent rate limit reached (%d/%d jobs in the last minute) — slow down or handle this task directly", recent, maxPerMin)
	}

	// Preflight: verify the backend CLI exists and is ready before queuing.
	if !skipSubagentPreflight {
		backends, err := loadServeBackends()
		if err != nil {
			fatal("preflight: failed to load backends: %v", err)
		}
		if b, ok := backends[args.backend]; !ok {
			fatal("preflight: unknown backend %q — available: %s", args.backend, availableBackendNames(backends))
		} else if err := oneagent.PreflightCheckBackend(args.backend, b); err != nil {
			fatal("preflight: %v", err)
		}
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
	if blocking {
		job.BlockingResultPath = subagentBlockingResultPath(job.ID)
	}
	store.WriteJob(job)

	if blocking {
		result, err := waitForBlockingSubagentResult(job)
		if err != nil {
			fatal("wait for nested subagent %s: %v", job.ID, err)
		}
		fmt.Print(result)
		return
	}

	fmt.Printf("subagent dispatched: %s\nbackend: %s\ndepth: %d/%d\ntask: %s\n", job.ID, args.backend, depth, maxDepth, args.text)
}

// --- Subagent list/show/cancel ---

func isSubagentJob(j store.PendingJob) bool {
	return j.Source == "subagent" || j.Source == "subagent-synthesis"
}

func isActiveSubagentJob(j store.PendingJob) bool {
	return j.Status == "running" || j.Status == ""
}

func jobDisplayStatus(status string) string {
	if status == "" {
		return "pending"
	}
	return status
}

func jobDisplayTask(j store.PendingJob, maxLen int) string {
	task := j.DelegatedTask
	if task == "" {
		task = j.Prompt
	}
	if len(task) > maxLen {
		task = task[:maxLen] + "..."
	}
	return task
}

func jobDisplayAge(updated time.Time) string {
	if updated.IsZero() {
		return ""
	}
	return formatAge(time.Since(updated))
}

func cmdSubagentList(args []string) {
	showAll := false
	for _, a := range args {
		if a == "--all" || a == "-a" {
			showAll = true
		}
	}

	jobs := store.ListJobs()
	found := false
	for _, j := range jobs {
		if !isSubagentJob(j) {
			continue
		}
		if !showAll && !isActiveSubagentJob(j) {
			continue
		}
		fmt.Printf("%-30s  %-10s  %-8s  %-6s  %s\n",
			j.ID, j.State.Backend, jobDisplayStatus(j.Status),
			jobDisplayAge(j.Updated), jobDisplayTask(j, 72))
		found = true
	}
	if !found {
		if showAll {
			fmt.Println("No subagent jobs found.")
		} else {
			fmt.Println("No active subagent jobs. Use --all to include completed/canceled.")
		}
	}
}

func printJobField(label, value string) {
	if value != "" {
		fmt.Printf("%-16s %s\n", label+":", value)
	}
}

func printJobSupervision(sup store.SupervisionState) {
	if sup.Attempt > 0 {
		printJobField("Attempt", fmt.Sprintf("%d/%d", sup.Attempt, sup.MaxAttempts))
	}
	printJobField("Run ID", sup.ActiveRunID)
	if !sup.LastEventAt.IsZero() {
		printJobField("Last Event", fmt.Sprintf("%s (%s ago)",
			sup.LastEventAt.Local().Format("15:04:05"),
			formatAge(time.Since(sup.LastEventAt))))
	}
	if !sup.LastProgressAt.IsZero() {
		printJobField("Last Progress", fmt.Sprintf("%s (%s ago)",
			sup.LastProgressAt.Local().Format("15:04:05"),
			formatAge(time.Since(sup.LastProgressAt))))
	}
	printJobField("Last Error", sup.LastError)
}

func cmdSubagentShow(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: moxie subagent show <job-id>")
		os.Exit(1)
	}
	j, ok := store.ReadJob(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "job not found: %s\n", args[0])
		os.Exit(1)
	}

	printJobField("ID", j.ID)
	printJobField("Status", jobDisplayStatus(j.Status))
	printJobField("Source", j.Source)
	printJobField("Backend", j.State.Backend)
	printJobField("Model", j.State.Model)
	printJobField("Thread", j.State.ThreadID)
	printJobField("Parent", j.ParentJobID)
	printJobField("Depth", fmt.Sprintf("%d", j.Depth))
	printJobSupervision(j.Supervision)
	printJobField("CWD", j.CWD)
	if !j.Updated.IsZero() {
		printJobField("Updated", j.Updated.Format(time.RFC3339))
	}
	printJobField("Conversation", j.ConversationID)
	if j.ReplyConversation != j.ConversationID {
		printJobField("Reply To", j.ReplyConversation)
	}

	task := jobDisplayTask(j, 500)
	if task != "" {
		fmt.Printf("\nTask:\n%s\n", task)
	}
}

func cmdSubagentCancel(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: moxie subagent cancel <job-id>")
		os.Exit(1)
	}
	jobID := args[0]
	j, ok := store.ReadJob(jobID)
	if !ok {
		fmt.Fprintf(os.Stderr, "job not found: %s\n", jobID)
		os.Exit(1)
	}
	if j.Status != "running" && j.Status != "" {
		fmt.Fprintf(os.Stderr, "job %s is not running (status: %s)\n", jobID, j.Status)
		os.Exit(1)
	}
	j.Status = "canceled"
	j.Updated = time.Now()
	store.WriteJob(j)
	fmt.Printf("canceled: %s\n", jobID)
}

// --- Subagent job watcher (serve loop) ---

// recentThreadTurnsContext returns a minimal context block from the last few turns.
func recentThreadTurnsContext(turns []oneagent.Turn, maxTurns, budget int) string {
	if maxTurns <= 0 {
		maxTurns = 2
	}
	if budget <= 0 {
		budget = 2048
	}

	var lines []string
	used := len("Conversation:\n")
	for i := len(turns) - 1; i >= 0; i-- {
		if len(lines) >= maxTurns {
			break
		}
		line := turns[i].Role + ": " + turns[i].Content
		if used+len(line)+1 > budget {
			break
		}
		lines = append(lines, line)
		used += len(line) + 1
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	if len(lines) == 0 {
		return ""
	}
	return "Conversation:\n" + strings.Join(lines, "\n")
}

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
	webexAPI       webexpkg.Messenger
	webexClient    *oneagent.Client
	schedules      *scheduler.Store
	maxDepth       int

	// inFlight tracks which job IDs are currently being processed so the
	// ticker doesn't re-dispatch them on the next tick.
	mu       sync.Mutex
	inFlight map[string]struct{}
}

func startSubagentWatcher(ctx context.Context, cfg store.Config, backends map[string]oneagent.Backend, schedules *scheduler.Store) {
	st := subagentTransports{
		schedules: schedules,
		maxDepth:  cfg.MaxSubagentDepth(),
		inFlight:  make(map[string]struct{}),
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
	if _, err := cfg.Webex(); err == nil {
		if adapter, err := webexpkg.New(&cfg, "", nil, nil); err == nil {
			st.webexAPI = adapter.API()
			st.webexClient = newWebexClient(backends)
		}
	}

	startTickerLoop(ctx, 3*time.Second, func() {
		runSubagentJobs(&st)
	})
}

func runSubagentJobs(st *subagentTransports) {
	jobs := store.ListJobs()
	for _, job := range jobs {
		if job.Source != "subagent" || (job.Status != "" && job.Status != "running" && job.Status != "ready") {
			continue
		}
		if dispatch.IsShuttingDown() {
			return
		}

		// Skip jobs that are already being processed by a previous tick.
		st.mu.Lock()
		if _, running := st.inFlight[job.ID]; running {
			st.mu.Unlock()
			continue
		}
		st.inFlight[job.ID] = struct{}{}
		st.mu.Unlock()

		job := job
		log.Printf("dispatching subagent job %s (backend=%s depth=%d/%d)", job.ID, job.State.Backend, job.Depth, st.maxDepth)

		provider := chat.ParseConversationID(job.ConversationID).Provider

		var client *oneagent.Client
		var deliver func(store.PendingJob) error

		switch provider {
		case chat.ProviderTelegram:
			if st.telegramBot == nil || st.telegramClient == nil {
				log.Printf("subagent job %s targets telegram but no telegram transport", job.ID)
				st.mu.Lock()
				delete(st.inFlight, job.ID)
				st.mu.Unlock()
				continue
			}
			client = st.telegramClient
			deliver = func(synthJob store.PendingJob) error {
				return botpkg.DeliverJobResult(st.telegramBot, &synthJob)
			}
		case chat.ProviderSlack:
			if st.slackAPI == nil || st.slackClient == nil {
				log.Printf("subagent job %s targets slack but no slack transport", job.ID)
				st.mu.Lock()
				delete(st.inFlight, job.ID)
				st.mu.Unlock()
				continue
			}
			client = st.slackClient
			deliver = func(synthJob store.PendingJob) error {
				return slackpkg.DeliverJobResult(st.slackAPI, &synthJob)
			}
		case chat.ProviderWebex:
			if st.webexAPI == nil || st.webexClient == nil {
				log.Printf("subagent job %s targets webex but no webex transport", job.ID)
				st.mu.Lock()
				delete(st.inFlight, job.ID)
				st.mu.Unlock()
				continue
			}
			client = st.webexClient
			deliver = func(synthJob store.PendingJob) error {
				return webexpkg.DeliverJobResult(st.webexAPI, &synthJob)
			}
		default:
			log.Printf("subagent job %s has unknown provider %s", job.ID, provider)
			st.mu.Lock()
			delete(st.inFlight, job.ID)
			st.mu.Unlock()
			continue
		}

		go func() {
			defer func() {
				st.mu.Lock()
				delete(st.inFlight, job.ID)
				st.mu.Unlock()
			}()
			dispatch.ProcessJob(&job, client, st.schedules, subagentCallbacks(
				job, client, st.schedules, deliver,
			))
		}()
	}
}

func subagentCallbacks(job store.PendingJob, synthClient *oneagent.Client, schedules *scheduler.Store, deliver func(store.PendingJob) error) dispatch.Callbacks {
	return dispatch.Callbacks{
		OnActivity:    func(string) {},
		OnStatusClear: func() {},
		OnDone:        func() {},
		OnResult: func(result string) error {
			if job.BlockingResultPath != "" {
				return writeBlockingSubagentResult(job, result)
			}
			return dispatchSynthesis(job, result, synthClient, schedules, deliver)
		},
	}
}

// dispatchSynthesis creates a synthesis job on the parent conversation's thread.
// It is intentionally queued instead of run inline so subagent completion does
// not block on the parent conversation lock. Delivery retry/recovery loops will
// pick it up and run it once the parent thread is available.
func dispatchSynthesis(subJob store.PendingJob, result string, client *oneagent.Client, schedules *scheduler.Store, deliver func(store.PendingJob) error) error {
	_ = client
	_ = schedules
	_ = deliver

	convState := subJob.SynthesisState
	if convState == (store.State{}) {
		convState = store.ReadConversationState(subJob.ConversationID)
	}
	prompt := buildSynthesisPrompt(subJob.DelegationContext, subJob.DelegatedTask, subJob.State.Backend, subJob.State.Model, result)
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
	log.Printf("queued synthesis job %s for subagent %s on thread %s", synthJob.ID, subJob.ID, convState.ThreadID)
	return nil
}

func buildSynthesisPrompt(delegationCtx, task, backend, model, result string) string {
	_ = delegationCtx
	if task == "" {
		task = "(unspecified)"
	}
	var b strings.Builder
	b.WriteString("Use this subagent result to reply to the user.\n\n")
	b.WriteString("Task: ")
	b.WriteString(task)
	b.WriteString("\nBackend: ")
	b.WriteString(backend)
	if model != "" {
		b.WriteString("\nModel: ")
		b.WriteString(model)
	}
	b.WriteString("\n\nResult:\n")
	b.WriteString(result)
	return b.String()
}
