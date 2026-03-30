package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/scheduler"
	"github.com/1broseidon/moxie/internal/store"
	workflowpkg "github.com/1broseidon/moxie/internal/workflow"
	"github.com/1broseidon/oneagent"
)

func workflowUsage() {
	fmt.Println(`moxie workflow — supervised multi-agent orchestration

Usage:
  moxie workflow run fanout --workers <backend[:model][,backend[:model]...]> --merge <backend[:model]> --text <task>
  moxie workflow list [--all]
  moxie workflow show <id>
  moxie workflow watch <id>
  moxie workflow cancel <id>

Subcommands:
  run               Start a workflow pattern (fanout implemented)
  list              List active workflows (use --all to include terminal workflows)
  show <id>         Show workflow details and step state
  watch <id>        Stream workflow events until completion
  cancel <id>       Cancel a workflow and mark child jobs canceled

Notes:
  Workflows currently run only from within an active dispatch
  Fanout launches bounded workers in parallel, then runs one merge step
  Final output is synthesized back into the parent conversation thread`)
}

type workflowRunArgs struct {
	pattern string
	workers []workflowpkg.AgentSpec
	merge   workflowpkg.AgentSpec
	text    string
	notify  string
}

func parseWorkflowRunArgs(args []string) (workflowRunArgs, error) {
	if len(args) == 0 {
		return workflowRunArgs{}, fmt.Errorf("missing workflow pattern")
	}
	pattern := strings.TrimSpace(args[0])
	if pattern != "fanout" {
		return workflowRunArgs{}, fmt.Errorf("unsupported workflow pattern: %s", pattern)
	}

	fs := flag.NewFlagSet("workflow run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workersRaw := fs.String("workers", "", "")
	merge := fs.String("merge", "", "")
	text := fs.String("text", "", "")
	notify := fs.String("notify", "silent", "")
	if err := fs.Parse(args[1:]); err != nil {
		return workflowRunArgs{}, err
	}
	workers, err := workflowpkg.ParseAgentSpecs(*workersRaw)
	if err != nil {
		return workflowRunArgs{}, fmt.Errorf("invalid --workers: %w", err)
	}
	if len(workers) == 0 {
		return workflowRunArgs{}, fmt.Errorf("fanout requires --workers")
	}
	mergeSpec, err := workflowpkg.ParseAgentSpec(strings.TrimSpace(*merge))
	if err != nil {
		return workflowRunArgs{}, fmt.Errorf("invalid --merge: %w", err)
	}
	if strings.TrimSpace(*text) == "" {
		return workflowRunArgs{}, fmt.Errorf("fanout requires --text")
	}
	return workflowRunArgs{
		pattern: pattern,
		workers: workers,
		merge:   mergeSpec,
		text:    strings.TrimSpace(*text),
		notify:  strings.TrimSpace(*notify),
	}, nil
}

func cmdWorkflow() {
	if len(os.Args) < 3 {
		workflowUsage()
		return
	}
	sub := os.Args[2]
	if sub == "help" || sub == "--help" || sub == "-h" {
		workflowUsage()
		return
	}
	switch sub {
	case "run":
		cmdWorkflowRun(os.Args[3:])
	case "list", "ls":
		cmdWorkflowList(os.Args[3:])
	case "show":
		cmdWorkflowShow(os.Args[3:])
	case "watch":
		cmdWorkflowWatch(os.Args[3:])
	case "cancel":
		cmdWorkflowCancel(os.Args[3:])
	default:
		workflowUsage()
	}
}

func cmdWorkflowRun(args []string) {
	runArgs, err := parseWorkflowRunArgs(args)
	if err != nil {
		fatal("%v", err)
	}
	parent := findParentJobOptional()
	if parent == nil {
		fatal("moxie workflow run currently requires a running dispatch")
	}

	wf, err := workflowpkg.BuildFanoutWorkflow(parent, workflowpkg.FanoutInput{
		Prompt:      runArgs.text,
		WorkerSpecs: runArgs.workers,
		MergeSpec:   runArgs.merge,
		Notify:      runArgs.notify,
	})
	if err != nil {
		fatal("build workflow: %v", err)
	}
	if err := store.WriteWorkflow(wf); err != nil {
		fatal("write workflow: %v", err)
	}
	if err := store.AppendWorkflowEvent(wf.ID, store.WorkflowEvent{Type: "workflow.created", Message: fmt.Sprintf("pattern=%s workers=%d merge=%s", wf.Pattern, len(runArgs.workers), formatAgentSpec(runArgs.merge))}); err != nil {
		log.Printf("workflow event write error for %s: %v", wf.ID, err)
	}

	workerLabels := make([]string, 0, len(runArgs.workers))
	for _, worker := range runArgs.workers {
		workerLabels = append(workerLabels, formatAgentSpec(worker))
	}
	fmt.Printf("workflow dispatched: %s\npattern: %s\nworkers: %s\nmerge: %s\ntask: %s\n", wf.ID, wf.Pattern, strings.Join(workerLabels, ", "), formatAgentSpec(runArgs.merge), wf.Prompt)
}

func cmdWorkflowList(args []string) {
	showAll := false
	for _, arg := range args {
		if arg == "--all" || arg == "-a" {
			showAll = true
		}
	}
	workflows := store.ListWorkflows()
	found := false
	for _, wf := range workflows {
		if !showAll && workflowIsTerminal(wf.Status) {
			continue
		}
		done, total := workflowDoneCounts(wf)
		fmt.Printf("%-24s  %-8s  %-10s  %-8s  %d/%d done  %s\n",
			wf.ID,
			wf.Pattern,
			workflowDisplayStatus(wf.Status),
			workflowAge(wf.Updated),
			done,
			total,
			truncateWorkflowPrompt(wf.Prompt, 64),
		)
		found = true
	}
	if !found {
		if showAll {
			fmt.Println("No workflows found.")
		} else {
			fmt.Println("No active workflows. Use --all to include completed workflows.")
		}
	}
}

func cmdWorkflowShow(args []string) {
	if len(args) == 0 {
		fatal("usage: moxie workflow show <id>")
	}
	wf, ok := store.ReadWorkflow(args[0])
	if !ok {
		fatal("workflow not found: %s", args[0])
	}
	printJobField("ID", wf.ID)
	printJobField("Pattern", wf.Pattern)
	printJobField("Status", workflowDisplayStatus(wf.Status))
	printJobField("Conversation", wf.ConversationID)
	printJobField("Reply To", wf.ReplyConversation)
	printJobField("Parent Job", wf.ParentJobID)
	printJobField("Parent Thread", wf.ParentThreadID)
	printJobField("Final Step", wf.FinalStepID)
	printJobField("Final Thread", wf.FinalThreadID)
	if !wf.Created.IsZero() {
		printJobField("Created", wf.Created.Format(time.RFC3339))
	}
	if !wf.Updated.IsZero() {
		printJobField("Updated", wf.Updated.Format(time.RFC3339))
	}
	printJobField("CWD", wf.CWD)
	printJobField("Notify", wf.Notify)
	printJobField("Last Error", wf.LastError)
	if wf.Prompt != "" {
		fmt.Printf("\nTask:\n%s\n", wf.Prompt)
	}
	if len(wf.Steps) > 0 {
		fmt.Printf("\nSteps:\n")
		for _, step := range wf.Steps {
			fmt.Printf("- %-8s  %-8s  %-10s  %s\n", step.ID, step.Role, workflowDisplayStatus(step.Status), step.Backend)
			if step.JobID != "" {
				fmt.Printf("  job: %s\n", step.JobID)
			}
			if step.ThreadID != "" {
				fmt.Printf("  thread: %s\n", step.ThreadID)
			}
			if step.LastError != "" {
				fmt.Printf("  error: %s\n", step.LastError)
			}
		}
	}
}

func cmdWorkflowWatch(args []string) {
	if len(args) == 0 {
		fatal("usage: moxie workflow watch <id>")
	}
	id := args[0]
	printed := 0
	for {
		events, err := store.ReadWorkflowEvents(id)
		if err != nil {
			fatal("read workflow events: %v", err)
		}
		for printed < len(events) {
			fmt.Println(formatWorkflowEvent(events[printed]))
			printed++
		}
		wf, ok := store.ReadWorkflow(id)
		if ok && workflowIsTerminal(wf.Status) && printed >= len(events) {
			return
		}
		if !ok && printed >= len(events) {
			fatal("workflow not found: %s", id)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func cmdWorkflowCancel(args []string) {
	if len(args) == 0 {
		fatal("usage: moxie workflow cancel <id>")
	}
	wf, ok := store.ReadWorkflow(args[0])
	if !ok {
		fatal("workflow not found: %s", args[0])
	}
	if workflowIsTerminal(wf.Status) {
		fmt.Printf("workflow already terminal: %s (%s)\n", wf.ID, wf.Status)
		return
	}
	wf.Status = "canceled"
	wf.LastError = "canceled by user"
	for i := range wf.Steps {
		if wf.Steps[i].Status == "running" || wf.Steps[i].Status == "pending" {
			wf.Steps[i].Status = "canceled"
		}
		if wf.Steps[i].JobID != "" {
			if job, ok := store.ReadJob(wf.Steps[i].JobID); ok {
				job.Status = "canceled"
				store.WriteJob(job)
			}
		}
	}
	if err := store.WriteWorkflow(wf); err != nil {
		fatal("write workflow: %v", err)
	}
	if err := store.AppendWorkflowEvent(wf.ID, store.WorkflowEvent{Type: "workflow.canceled", Message: "canceled by user"}); err != nil {
		log.Printf("workflow event write error for %s: %v", wf.ID, err)
	}
	fmt.Printf("canceled: %s\n", wf.ID)
}

func workflowDisplayStatus(status string) string {
	if strings.TrimSpace(status) == "" {
		return "pending"
	}
	return status
}

func workflowIsTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "canceled":
		return true
	default:
		return false
	}
}

func workflowDoneCounts(wf store.Workflow) (int, int) {
	done := 0
	for _, step := range wf.Steps {
		if step.Status == "completed" {
			done++
		}
	}
	return done, len(wf.Steps)
}

func workflowAge(updated time.Time) string {
	if updated.IsZero() {
		return ""
	}
	return formatAge(time.Since(updated))
}

func truncateWorkflowPrompt(prompt string, max int) string {
	prompt = strings.TrimSpace(prompt)
	if len(prompt) <= max {
		return prompt
	}
	return prompt[:max] + "..."
}

func formatWorkflowEvent(ev store.WorkflowEvent) string {
	parts := []string{ev.TS.Local().Format("15:04:05"), ev.Type}
	if ev.StepID != "" {
		parts = append(parts, "step="+ev.StepID)
	}
	if ev.Message != "" {
		parts = append(parts, ev.Message)
	}
	return strings.Join(parts, "  ")
}

type workflowTransports struct {
	telegramClient *oneagent.Client
	slackClient    *oneagent.Client
	webexClient    *oneagent.Client
	schedules      *scheduler.Store

	mu       sync.Mutex
	inFlight map[string]struct{}
}

func startWorkflowWatcher(ctx context.Context, cfg store.Config, backends map[string]oneagent.Backend, schedules *scheduler.Store) {
	st := &workflowTransports{
		schedules: schedules,
		inFlight:  make(map[string]struct{}),
	}
	if _, err := cfg.Telegram(); err == nil {
		st.telegramClient = newTelegramClient(backends)
	}
	if _, err := cfg.Slack(); err == nil {
		st.slackClient = newSlackClient(backends)
	}
	if _, err := cfg.Webex(); err == nil {
		st.webexClient = newWebexClient(backends)
	}
	startTickerLoop(ctx, 3*time.Second, func() {
		runWorkflowJobs(st)
	})
}

func runWorkflowJobs(st *workflowTransports) {
	for _, wf := range store.ListWorkflows() {
		if workflowIsTerminal(wf.Status) {
			continue
		}
		client := st.clientForConversation(wf.ConversationID)
		if client == nil {
			log.Printf("workflow %s has no configured client for %s", wf.ID, wf.ConversationID)
			continue
		}
		for _, step := range wf.Steps {
			if step.Status != "running" || step.JobID == "" {
				continue
			}
			job, ok := store.ReadJob(step.JobID)
			if !ok {
				markWorkflowStepFailed(st, wf.ID, step.ID, "workflow step job missing during recovery", nil)
				continue
			}
			st.processWorkflowStep(wf.ID, step.ID, job, client)
		}
		for _, stepID := range workflowpkg.ReadyStepIDs(wf) {
			step, ok := workflowStepByID(wf, stepID)
			if !ok {
				continue
			}
			job := buildWorkflowStepJob(wf, step)
			setWorkflowStepRunning(st, wf.ID, step.ID, job)
			store.WriteJob(job)
			st.processWorkflowStep(wf.ID, step.ID, job, client)
		}
	}
}

func (st *workflowTransports) clientForConversation(conversationID string) *oneagent.Client {
	switch chat.ParseConversationID(conversationID).Provider {
	case chat.ProviderTelegram:
		return st.telegramClient
	case chat.ProviderSlack:
		return st.slackClient
	case chat.ProviderWebex:
		return st.webexClient
	default:
		return nil
	}
}

func (st *workflowTransports) processWorkflowStep(workflowID, stepID string, job store.PendingJob, client *oneagent.Client) {
	key := workflowStepKey(workflowID, stepID)
	if !st.claimStep(key) {
		return
	}
	go func() {
		defer st.releaseStep(key)
		dispatch.ProcessJob(&job, client, st.schedules, dispatch.Callbacks{
			OnActivity: func(activity string) {
				recordWorkflowStepActivity(st, workflowID, stepID, &job, activity)
			},
			OnStatusClear: func() {},
			OnDone:        func() {},
			OnResult: func(result string) error {
				finalizeWorkflowStep(st, workflowID, stepID, &job, result)
				return nil
			},
		})
	}()
}

func (st *workflowTransports) claimStep(key string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.inFlight[key]; ok {
		return false
	}
	st.inFlight[key] = struct{}{}
	return true
}

func (st *workflowTransports) releaseStep(key string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.inFlight, key)
}

func setWorkflowStepRunning(st *workflowTransports, workflowID, stepID string, job store.PendingJob) {
	st.mu.Lock()
	defer st.mu.Unlock()
	wf, ok := store.ReadWorkflow(workflowID)
	if !ok {
		return
	}
	idx := workflowStepIndex(wf, stepID)
	if idx < 0 {
		return
	}
	step := &wf.Steps[idx]
	step.Status = "running"
	step.JobID = job.ID
	step.ThreadID = job.State.ThreadID
	step.StartedAt = time.Now()
	step.LastProgressAt = time.Time{}
	step.LastError = ""
	step.Result = ""
	step.RetryCount = 0
	if step.Role == "merge" {
		wf.Status = "merging"
	} else {
		wf.Status = "running"
	}
	if err := store.WriteWorkflow(wf); err != nil {
		log.Printf("workflow write error for %s: %v", workflowID, err)
	}
	if err := store.AppendWorkflowEvent(workflowID, store.WorkflowEvent{Type: "step.started", StepID: stepID, Message: "backend=" + job.State.Backend}); err != nil {
		log.Printf("workflow event write error for %s: %v", workflowID, err)
	}
}

func recordWorkflowStepActivity(st *workflowTransports, workflowID, stepID string, job *store.PendingJob, activity string) {
	message := normalizeWorkflowActivity(activity)
	if message == "" {
		return
	}
	progressAt := time.Now()
	if job != nil && !job.Supervision.LastProgressAt.IsZero() {
		progressAt = job.Supervision.LastProgressAt
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	wf, ok := store.ReadWorkflow(workflowID)
	if !ok || workflowIsTerminal(wf.Status) {
		return
	}
	idx := workflowStepIndex(wf, stepID)
	if idx < 0 {
		return
	}
	step := &wf.Steps[idx]
	if step.Status != "running" {
		return
	}
	step.LastProgressAt = progressAt
	if err := store.WriteWorkflow(wf); err != nil {
		log.Printf("workflow write error for %s: %v", workflowID, err)
	}
	if err := store.AppendWorkflowEvent(workflowID, store.WorkflowEvent{Type: "step.progress", StepID: stepID, Message: message, TS: progressAt}); err != nil {
		log.Printf("workflow event write error for %s: %v", workflowID, err)
	}
}

func finalizeWorkflowStep(st *workflowTransports, workflowID, stepID string, job *store.PendingJob, result string) {
	st.mu.Lock()
	wf, ok := store.ReadWorkflow(workflowID)
	if !ok {
		st.mu.Unlock()
		return
	}
	if workflowIsTerminal(wf.Status) && wf.Status != "merging" {
		st.mu.Unlock()
		return
	}
	idx := workflowStepIndex(wf, stepID)
	if idx < 0 {
		st.mu.Unlock()
		return
	}
	step := &wf.Steps[idx]
	step.JobID = job.ID
	step.ThreadID = job.State.ThreadID
	step.FinishedAt = time.Now()
	step.LastProgressAt = job.Supervision.LastProgressAt
	step.RetryCount = max(0, job.Supervision.Attempt-1)
	step.Result = result

	failed := workflowStepFailed(*job, result)
	var queuedResult string
	if failed {
		step.Status = "failed"
		step.LastError = workflowStepError(*job, result)
		wf.Status = "failed"
		wf.LastError = step.LastError
		queuedResult = workflowFailureResult(wf, *step)
	} else {
		step.Status = "completed"
		step.LastError = ""
		if step.Role == "merge" {
			wf.Status = "completed"
			wf.FinalThreadID = step.ThreadID
			queuedResult = result
		} else {
			wf.Status = "running"
		}
	}
	if err := store.WriteWorkflow(wf); err != nil {
		log.Printf("workflow write error for %s: %v", workflowID, err)
	}
	eventType := "step.completed"
	eventMsg := "backend=" + step.Backend
	if failed {
		eventType = "step.failed"
		eventMsg = step.LastError
	}
	if err := store.AppendWorkflowEvent(workflowID, store.WorkflowEvent{Type: eventType, StepID: stepID, Message: eventMsg}); err != nil {
		log.Printf("workflow event write error for %s: %v", workflowID, err)
	}
	if failed {
		if err := store.AppendWorkflowEvent(workflowID, store.WorkflowEvent{Type: "workflow.failed", Message: wf.LastError}); err != nil {
			log.Printf("workflow event write error for %s: %v", workflowID, err)
		}
	} else if step.Role == "merge" {
		if err := store.AppendWorkflowEvent(workflowID, store.WorkflowEvent{Type: "workflow.completed", Message: "final_thread=" + wf.FinalThreadID}); err != nil {
			log.Printf("workflow event write error for %s: %v", workflowID, err)
		}
	}
	st.mu.Unlock()

	if queuedResult != "" {
		queueWorkflowSynthesisResult(wf, step.Backend, step.Model, queuedResult)
	}
}

func markWorkflowStepFailed(st *workflowTransports, workflowID, stepID, reason string, job *store.PendingJob) {
	st.mu.Lock()
	defer st.mu.Unlock()
	wf, ok := store.ReadWorkflow(workflowID)
	if !ok {
		return
	}
	idx := workflowStepIndex(wf, stepID)
	if idx < 0 {
		return
	}
	step := &wf.Steps[idx]
	step.Status = "failed"
	step.LastError = reason
	step.FinishedAt = time.Now()
	if job != nil {
		step.JobID = job.ID
		step.ThreadID = job.State.ThreadID
	}
	wf.Status = "failed"
	wf.LastError = reason
	if err := store.WriteWorkflow(wf); err != nil {
		log.Printf("workflow write error for %s: %v", workflowID, err)
	}
	if err := store.AppendWorkflowEvent(workflowID, store.WorkflowEvent{Type: "step.failed", StepID: stepID, Message: reason}); err != nil {
		log.Printf("workflow event write error for %s: %v", workflowID, err)
	}
	if err := store.AppendWorkflowEvent(workflowID, store.WorkflowEvent{Type: "workflow.failed", Message: reason}); err != nil {
		log.Printf("workflow event write error for %s: %v", workflowID, err)
	}
}

func buildWorkflowStepJob(wf store.Workflow, step store.WorkflowStep) store.PendingJob {
	threadID := step.ThreadID
	if threadID == "" {
		threadID = workflowStepThreadID(wf.ID, step.ID)
	}
	prompt := step.Prompt
	source := "workflow-step"
	if step.Role == "merge" {
		prompt = workflowpkg.BuildMergePrompt(wf)
		source = "workflow-merge"
	}
	depth := 1
	if parent, ok := store.ReadJob(wf.ParentJobID); ok {
		depth = parent.Depth + 1
	}
	jobID := step.JobID
	if jobID == "" {
		jobID = store.NewJobID()
	}
	return store.PendingJob{
		ID:                jobID,
		ParentJobID:       wf.ParentJobID,
		DelegatedTask:     wf.Prompt,
		ReplyConversation: wf.ReplyConversation,
		ConversationID:    wf.ConversationID,
		Source:            source,
		Prompt:            prompt,
		CWD:               wf.CWD,
		Depth:             depth,
		SynthesisState:    wf.State,
		State: store.State{
			Backend:  step.Backend,
			Model:    step.Model,
			ThreadID: threadID,
			Thinking: portableWorkflowThinking(wf.State.Thinking),
		},
	}
}

func portableWorkflowThinking(raw string) string {
	level := strings.ToLower(strings.TrimSpace(raw))
	switch level {
	case "", "off":
		return ""
	case "low", "medium", "high", "max":
		return level
	default:
		return ""
	}
}

func normalizeWorkflowActivity(activity string) string {
	activity = strings.Join(strings.Fields(strings.TrimSpace(activity)), " ")
	if activity == "" {
		return ""
	}
	const maxLen = 160
	if len(activity) <= maxLen {
		return activity
	}
	return strings.TrimSpace(activity[:maxLen-1]) + "…"
}

func workflowStepKey(workflowID, stepID string) string {
	return workflowID + ":" + stepID
}

func workflowStepThreadID(workflowID, stepID string) string {
	return workflowID + "-" + stepID
}

func workflowStepByID(wf store.Workflow, stepID string) (store.WorkflowStep, bool) {
	for _, step := range wf.Steps {
		if step.ID == stepID {
			return step, true
		}
	}
	return store.WorkflowStep{}, false
}

func workflowStepIndex(wf store.Workflow, stepID string) int {
	for i, step := range wf.Steps {
		if step.ID == stepID {
			return i
		}
	}
	return -1
}

func workflowStepFailed(job store.PendingJob, result string) bool {
	if strings.TrimSpace(job.Supervision.LastError) != "" {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(result), "preflight error:")
}

func workflowStepError(job store.PendingJob, result string) string {
	if errText := strings.TrimSpace(job.Supervision.LastError); errText != "" {
		return errText
	}
	if trimmed := strings.TrimSpace(result); trimmed != "" {
		return trimmed
	}
	return "workflow step failed"
}

func workflowFailureResult(wf store.Workflow, step store.WorkflowStep) string {
	backend := strings.TrimSpace(step.Backend)
	if backend == "" {
		backend = "unknown"
	}
	task := strings.TrimSpace(wf.Prompt)
	if task == "" {
		task = "(unspecified)"
	}
	errText := strings.TrimSpace(step.LastError)
	if errText == "" {
		errText = "unknown failure"
	}
	return fmt.Sprintf("Subagent failed while gathering results.\nBackend: %s\nTask: %s\nLast observed error: %s\n\nWould you like me to retry, narrow the task, switch backend, or handle it directly?", backend, task, errText)
}

func queueWorkflowSynthesisResult(wf store.Workflow, backend, model, result string) {
	job := store.PendingJob{
		ConversationID:    wf.ConversationID,
		ReplyConversation: wf.ReplyConversation,
		DelegatedTask:     wf.Prompt,
		CWD:               wf.CWD,
		SynthesisState:    wf.State,
		State: store.State{
			Backend: backend,
			Model:   model,
		},
	}
	if err := dispatchSynthesis(job, result, nil, nil, nil); err != nil {
		log.Printf("workflow synthesis queue error for %s: %v", wf.ID, err)
	}
}

func formatAgentSpec(spec workflowpkg.AgentSpec) string {
	if strings.TrimSpace(spec.Model) == "" {
		return strings.TrimSpace(spec.Backend)
	}
	return strings.TrimSpace(spec.Backend) + ":" + strings.TrimSpace(spec.Model)
}
