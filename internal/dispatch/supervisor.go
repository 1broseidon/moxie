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
	return client != nil && job != nil && job.Source == "subagent"
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
	return lastErr, false
}

func runSupervisedAttempt(job *store.PendingJob, client *oneagent.Client, stallTimeout, progressTimeout time.Duration, onActivity func(string)) (string, string, bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
			if cancelReason != "" {
				continue
			}
			applySupervisionEvent(job, event, onActivity)
		case resp := <-respCh:
			if cancelReason != "" {
				if IsShuttingDown() && IsShutdownError(resp.Error) {
					return "", "", true
				}
				return "", cancelReason, false
			}
			if resp.Error != "" {
				if IsShuttingDown() && IsShutdownError(resp.Error) {
					return "", "", true
				}
				job.Supervision.LastError = resp.Error
				store.WriteJob(*job)
				return "", resp.Error, false
			}
			return normalizeModelResult(resp.Result), "", false
		case <-ticker.C:
			if cancelReason != "" {
				continue
			}

			now := time.Now()
			lastEventAt := job.Supervision.LastEventAt
			if lastEventAt.IsZero() {
				lastEventAt = startedAt
			}
			if stallTimeout > 0 && now.Sub(lastEventAt) > stallTimeout {
				cancelReason = fmt.Sprintf("subagent stalled: no events for %s", stallTimeout)
				job.Supervision.LastError = cancelReason
				store.WriteJob(*job)
				cancel()
				continue
			}

			lastProgressAt := job.Supervision.LastProgressAt
			if lastProgressAt.IsZero() {
				lastProgressAt = startedAt
			}
			if progressTimeout > 0 && now.Sub(lastProgressAt) > progressTimeout {
				cancelReason = fmt.Sprintf("subagent stalled: no progress for %s", progressTimeout)
				job.Supervision.LastError = cancelReason
				store.WriteJob(*job)
				cancel()
			}
		}
	}
}

func applySupervisionEvent(job *store.PendingJob, event oneagent.StreamEvent, onActivity func(string)) bool {
	activeRunID := strings.TrimSpace(job.Supervision.ActiveRunID)
	eventRunID := strings.TrimSpace(event.RunID)
	if activeRunID != "" && eventRunID != "" && eventRunID != activeRunID {
		log.Printf("ignoring stale subagent event for %s: run_id=%s active_run_id=%s type=%s", job.ID, eventRunID, activeRunID, event.Type)
		return false
	}
	if activeRunID == "" && eventRunID != "" {
		job.Supervision.ActiveRunID = eventRunID
	}

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

	if event.Type == "activity" && strings.TrimSpace(event.Activity) != "" {
		log.Printf("[%s] %s", job.State.Backend, event.Activity)
		if onActivity != nil {
			onActivity(event.Activity)
		}
	}
	return true
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
