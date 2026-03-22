package dispatch

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/1broseidon/moxie/internal/store"
)

const (
	defaultBlockingSubagentFailureReason  = "subagent interrupted before producing a normal result"
	shutdownBlockingSubagentFailureReason = "subagent interrupted during shutdown before producing a normal result"
)

// WriteBlockingSubagentResult resolves a blocking nested subagent by writing its
// result to the sentinel file the waiting parent process is polling.
func WriteBlockingSubagentResult(job store.PendingJob, result string) error {
	if strings.TrimSpace(job.BlockingResultPath) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(job.BlockingResultPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(job.BlockingResultPath, []byte(result), 0o600); err != nil {
		return err
	}
	log.Printf("wrote blocking subagent result for %s", job.ID)
	return nil
}

func finalizeBlockingSubagentFailure(job *store.PendingJob, reason string) bool {
	if job == nil || strings.TrimSpace(job.BlockingResultPath) == "" {
		return false
	}
	result := buildBlockingSubagentFailureResult(job, reason)
	if err := WriteBlockingSubagentResult(*job, result); err != nil {
		log.Printf("blocking subagent failure write error for %s: %v", job.ID, err)
		return false
	}
	store.CleanupJobTemp(*job)
	store.RemoveJob(job.ID)
	return true
}

func buildBlockingSubagentFailureResult(job *store.PendingJob, reason string) string {
	backend := "unknown"
	if job != nil && strings.TrimSpace(job.State.Backend) != "" {
		backend = strings.TrimSpace(job.State.Backend)
	}
	task := summarizeSupervisedTask(job)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = defaultBlockingSubagentFailureReason
	}
	return fmt.Sprintf("Blocking subagent failed before producing a normal result.\nBackend: %s\nTask: %s\nReason: %s\n\nRetry the delegation, switch backend, or handle this task directly.", backend, task, reason)
}

func blockingSubagentInterruptReason() string {
	if IsShuttingDown() {
		return shutdownBlockingSubagentFailureReason
	}
	return defaultBlockingSubagentFailureReason
}
