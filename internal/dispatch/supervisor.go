package dispatch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

type runStreamModelFuncType func(context.Context, *store.PendingJob, *oneagent.Client, func(oneagent.StreamEvent)) oneagent.Response

var (
	runStreamModelFunc      runStreamModelFuncType = RunModelStreamContext
	supervisionPollInterval                        = 250 * time.Millisecond
)

func SetRunStreamModelFuncForTest(fn func(context.Context, *store.PendingJob, *oneagent.Client, func(oneagent.StreamEvent)) oneagent.Response) func() {
	prev := runStreamModelFunc
	if fn == nil {
		runStreamModelFunc = RunModelStreamContext
	} else {
		runStreamModelFunc = fn
	}
	return func() {
		runStreamModelFunc = prev
	}
}

func SetSupervisionPollIntervalForTest(d time.Duration) func() {
	prev := supervisionPollInterval
	if d > 0 {
		supervisionPollInterval = d
	}
	return func() {
		supervisionPollInterval = prev
	}
}

func RunModelStreamContext(ctx context.Context, job *store.PendingJob, client *oneagent.Client, emit func(oneagent.StreamEvent)) oneagent.Response {
	maybeClearStalePiSession(client, job)

	resp := runBackendWithContext(ctx, job, client, emit)
	if resp.Error != "" && job != nil && job.State.Backend == "pi" && IsMissingNativeSessionError(resp.Error) {
		if ClearNativeSession(client, job.State) {
			log.Printf("cleared missing pi native session for %s; retrying once", job.State.ThreadID)
			resp = runBackendWithContext(ctx, job, client, emit)
		}
	}
	return resp
}

func isSupervisedSubagentJob(job *store.PendingJob, client *oneagent.Client) bool {
	if client == nil || job == nil {
		return false
	}
	switch job.Source {
	case "subagent", "workflow-step", "workflow-merge":
		return true
	default:
		return false
	}
}

func runSupervisedSubagent(job *store.PendingJob, client *oneagent.Client, onActivity func(string)) (string, bool) {
	cfg := loadSupervisionConfig()
	maxAttempts := cfg.MaxSubagentAttempts()
	stallTimeout := cfg.SubagentStallDuration()
	progressTimeout := cfg.SubagentProgressDuration()
	backoff := cfg.SubagentRetryBackoffDurations()

	baseThreadID := strings.TrimSpace(job.State.ThreadID)
	if baseThreadID == "" {
		baseThreadID = freshSubagentThreadID(job.ID, 1)
		job.State.ThreadID = baseThreadID
	}

	job.Supervision.MaxAttempts = maxAttempts
	store.WriteJob(*job)

	var lastErr string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			if wait := backoffForAttempt(backoff, attempt); wait > 0 {
				log.Printf("backing off subagent job %s for %s before attempt %d/%d", job.ID, wait, attempt, maxAttempts)
				if sleepWithShutdown(wait) {
					return "", true
				}
			}
			job.State.ThreadID = freshSubagentThreadID(baseThreadID, attempt)
		}

		resetSupervisionAttempt(job, attempt, maxAttempts)
		store.WriteJob(*job)

		result, errText, interrupted := runSupervisedAttempt(job, client, stallTimeout, progressTimeout, onActivity)
		if interrupted {
			return "", true
		}
		if errText == "" {
			return result, false
		}

		lastErr = errText
		job.Supervision.LastError = errText
		store.WriteJob(*job)

		if attempt < maxAttempts {
			log.Printf("subagent job %s attempt %d/%d failed: %s", job.ID, attempt, maxAttempts, errText)
		}
	}

	if lastErr == "" {
		lastErr = fmt.Sprintf("subagent failed after %d attempts", maxAttempts)
	}
	escalation := buildSupervisedFailureMessage(job, maxAttempts, lastErr)
	job.Supervision.LastError = lastErr
	store.WriteJob(*job)
	log.Printf("subagent job %s exhausted supervision attempts: %s", job.ID, lastErr)
	return escalation, false
}

func runSupervisedAttempt(job *store.PendingJob, client *oneagent.Client, stallTimeout, progressTimeout time.Duration, onActivity func(string)) (string, string, bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	attempt := job.Supervision.Attempt
	attemptThreadID := strings.TrimSpace(job.State.ThreadID)
	events := make(chan oneagent.StreamEvent, 64)
	respCh := make(chan oneagent.Response, 1)
	go func() {
		respCh <- runStreamModelFunc(ctx, job, client, func(event oneagent.StreamEvent) {
			select {
			case events <- event:
			case <-ctx.Done():
			}
		})
	}()

	startedAt := time.Now()
	ticker := time.NewTicker(supervisionPollInterval)
	defer ticker.Stop()

	cancelReason := ""
	for {
		select {
		case event := <-events:
			handleSupervisionEvent(job, attempt, attemptThreadID, event, onActivity, cancelReason)
		case resp := <-respCh:
			return finalizeSupervisedAttempt(job, attempt, attemptThreadID, resp, cancelReason)
		case <-ticker.C:
			cancelReason = updateSupervisionCancelReason(job, startedAt, stallTimeout, progressTimeout, cancelReason, cancel)
		}
	}
}

func handleSupervisionEvent(job *store.PendingJob, attempt int, attemptThreadID string, event oneagent.StreamEvent, onActivity func(string), cancelReason string) {
	if cancelReason != "" {
		return
	}
	applySupervisionEvent(job, attempt, attemptThreadID, event, onActivity)
}

func finalizeSupervisedAttempt(job *store.PendingJob, attempt int, attemptThreadID string, resp oneagent.Response, cancelReason string) (string, string, bool) {
	if cancelReason != "" {
		log.Printf("suppressing canceled subagent attempt result for %s attempt %d/%d thread=%s", job.ID, attempt, job.Supervision.MaxAttempts, attemptThreadID)
		if IsShuttingDown() && IsShutdownError(resp.Error) {
			return "", "", true
		}
		return "", cancelReason, false
	}
	if shouldIgnoreSupervisionResponse(job, attempt, attemptThreadID, resp) {
		if errText := strings.TrimSpace(job.Supervision.LastError); errText != "" {
			return "", errText, false
		}
		return "", "stale subagent attempt result suppressed", false
	}
	if resp.Error == "" {
		return normalizeModelResult(resp.Result), "", false
	}
	if IsShuttingDown() && IsShutdownError(resp.Error) {
		return "", "", true
	}
	job.Supervision.LastError = resp.Error
	store.WriteJob(*job)
	return "", resp.Error, false
}

func updateSupervisionCancelReason(job *store.PendingJob, startedAt time.Time, stallTimeout, progressTimeout time.Duration, cancelReason string, cancel context.CancelFunc) string {
	if cancelReason != "" {
		return cancelReason
	}
	if reason := supervisionStallReason(job.Supervision.LastEventAt, startedAt, stallTimeout, "events"); reason != "" {
		job.Supervision.LastError = reason
		store.WriteJob(*job)
		cancel()
		return reason
	}
	if reason := supervisionStallReason(job.Supervision.LastProgressAt, startedAt, progressTimeout, "progress"); reason != "" {
		job.Supervision.LastError = reason
		store.WriteJob(*job)
		cancel()
		return reason
	}
	return ""
}

func supervisionStallReason(lastSeenAt, startedAt time.Time, timeout time.Duration, kind string) string {
	if timeout <= 0 {
		return ""
	}
	if lastSeenAt.IsZero() {
		lastSeenAt = startedAt
	}
	if time.Since(lastSeenAt) <= timeout {
		return ""
	}
	return fmt.Sprintf("subagent stalled: no %s for %s", kind, timeout)
}

func applySupervisionEvent(job *store.PendingJob, attempt int, attemptThreadID string, event oneagent.StreamEvent, onActivity func(string)) bool {
	if shouldIgnoreSupervisionEvent(job, attempt, attemptThreadID, event) {
		return false
	}
	bindSupervisionRunID(job, event)
	recordSupervisionEvent(job, event)
	emitSupervisionActivity(job, event, onActivity)
	return true
}

func shouldIgnoreSupervisionEvent(job *store.PendingJob, attempt int, attemptThreadID string, event oneagent.StreamEvent) bool {
	if !isActiveSupervisionAttempt(job, attempt, attemptThreadID) {
		log.Printf("ignoring stale subagent event for %s: attempt=%d active_attempt=%d thread=%s active_thread=%s type=%s", job.ID, attempt, job.Supervision.Attempt, attemptThreadID, job.State.ThreadID, event.Type)
		return true
	}
	if isMismatchedSupervisionThread(job.State.ThreadID, event.ThreadID) {
		log.Printf("ignoring stale subagent event for %s: thread_id=%s active_thread_id=%s type=%s", job.ID, event.ThreadID, job.State.ThreadID, event.Type)
		return true
	}
	activeRunID := strings.TrimSpace(job.Supervision.ActiveRunID)
	eventRunID := strings.TrimSpace(event.RunID)
	if activeRunID != "" && eventRunID != "" && eventRunID != activeRunID {
		log.Printf("ignoring stale subagent event for %s: run_id=%s active_run_id=%s type=%s", job.ID, eventRunID, activeRunID, event.Type)
		return true
	}
	return false
}

func bindSupervisionRunID(job *store.PendingJob, event oneagent.StreamEvent) {
	if strings.TrimSpace(job.Supervision.ActiveRunID) == "" && strings.TrimSpace(event.RunID) != "" {
		job.Supervision.ActiveRunID = strings.TrimSpace(event.RunID)
	}
}

func recordSupervisionEvent(job *store.PendingJob, event oneagent.StreamEvent) {
	ts := event.TS
	if ts.IsZero() {
		ts = time.Now()
	}
	job.Supervision.LastEventAt = ts
	if isProgressEvent(event.Type) {
		job.Supervision.LastProgressAt = ts
	}
	if event.Type == "error" && strings.TrimSpace(event.Error) != "" {
		job.Supervision.LastError = event.Error
	}
	store.WriteJob(*job)
}

func emitSupervisionActivity(job *store.PendingJob, event oneagent.StreamEvent, onActivity func(string)) {
	if event.Type != "activity" || strings.TrimSpace(event.Activity) == "" {
		return
	}
	log.Printf("[%s] %s", job.State.Backend, event.Activity)
	if onActivity != nil {
		onActivity(event.Activity)
	}
}

func isMismatchedSupervisionThread(activeThreadID, candidateThreadID string) bool {
	activeThreadID = strings.TrimSpace(activeThreadID)
	candidateThreadID = strings.TrimSpace(candidateThreadID)
	return activeThreadID != "" && candidateThreadID != "" && candidateThreadID != activeThreadID
}

func shouldIgnoreSupervisionResponse(job *store.PendingJob, attempt int, attemptThreadID string, resp oneagent.Response) bool {
	if !isActiveSupervisionAttempt(job, attempt, attemptThreadID) {
		log.Printf("ignoring stale subagent response for %s: attempt=%d active_attempt=%d response_thread=%s active_thread=%s", job.ID, attempt, job.Supervision.Attempt, resp.ThreadID, job.State.ThreadID)
		return true
	}

	activeThreadID := strings.TrimSpace(job.State.ThreadID)
	responseThreadID := strings.TrimSpace(resp.ThreadID)
	if isMismatchedSupervisionThread(activeThreadID, responseThreadID) {
		log.Printf("ignoring stale subagent response for %s: thread_id=%s active_thread_id=%s", job.ID, responseThreadID, activeThreadID)
		return true
	}
	return false
}

func isActiveSupervisionAttempt(job *store.PendingJob, attempt int, attemptThreadID string) bool {
	if job == nil {
		return false
	}
	if attempt > 0 && job.Supervision.Attempt > 0 && job.Supervision.Attempt != attempt {
		return false
	}
	activeThreadID := strings.TrimSpace(job.State.ThreadID)
	attemptThreadID = strings.TrimSpace(attemptThreadID)
	return activeThreadID == "" || attemptThreadID == "" || activeThreadID == attemptThreadID
}

func buildSupervisedFailureMessage(job *store.PendingJob, attempts int, lastErr string) string {
	backend := "unknown"
	if job != nil && strings.TrimSpace(job.State.Backend) != "" {
		backend = strings.TrimSpace(job.State.Backend)
	}
	task := summarizeSupervisedTask(job)
	lastErr = strings.TrimSpace(lastErr)
	if lastErr == "" {
		lastErr = "unknown failure"
	}

	return fmt.Sprintf("Subagent failed after %d/%d supervised attempts.\nBackend: %s\nTask: %s\nLast observed error: %s\n\nWould you like me to retry, switch backend, narrow the task, or handle it directly?", attempts, attempts, backend, task, lastErr)
}

func summarizeSupervisedTask(job *store.PendingJob) string {
	if job == nil {
		return "(unspecified)"
	}
	task := strings.TrimSpace(job.DelegatedTask)
	if task == "" {
		task = strings.TrimSpace(job.Prompt)
	}
	if task == "" {
		return "(unspecified)"
	}
	task = strings.Join(strings.Fields(task), " ")
	const maxTaskLen = 160
	if len(task) <= maxTaskLen {
		return task
	}
	return strings.TrimSpace(task[:maxTaskLen-1]) + "…"
}

func isProgressEvent(eventType string) bool {
	switch eventType {
	case "start", "activity", "delta", "session", "done":
		return true
	default:
		return false
	}
}

func resetSupervisionAttempt(job *store.PendingJob, attempt, maxAttempts int) {
	job.Supervision.Attempt = attempt
	job.Supervision.MaxAttempts = maxAttempts
	job.Supervision.ActiveRunID = ""
	job.Supervision.LastEventAt = time.Time{}
	job.Supervision.LastProgressAt = time.Time{}
	job.Supervision.LastError = ""
}

func loadSupervisionConfig() store.Config {
	cfg, err := store.LoadConfig()
	if err != nil {
		return store.Config{}
	}
	return cfg
}

func backoffForAttempt(backoff []time.Duration, attempt int) time.Duration {
	if attempt <= 1 || len(backoff) == 0 {
		return 0
	}
	idx := attempt - 1
	if idx >= len(backoff) {
		return backoff[len(backoff)-1]
	}
	return backoff[idx]
}

func sleepWithShutdown(wait time.Duration) bool {
	if wait <= 0 {
		return IsShuttingDown()
	}
	deadline := time.Now().Add(wait)
	for {
		if IsShuttingDown() {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		step := remaining
		if step > 100*time.Millisecond {
			step = 100 * time.Millisecond
		}
		time.Sleep(step)
	}
}

func freshSubagentThreadID(base string, attempt int) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "subagent"
	}
	return fmt.Sprintf("%s-attempt-%d-%d", base, attempt, time.Now().UnixNano())
}
