package workflow

import (
	"fmt"
	"strings"
	"time"

	"github.com/1broseidon/moxie/internal/store"
)

type AgentSpec struct {
	Backend string
	Model   string
}

type FanoutInput struct {
	Prompt      string
	WorkerSpecs []AgentSpec
	MergeSpec   AgentSpec
	Notify      string
}

func ParseAgentSpec(raw string) (AgentSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return AgentSpec{}, fmt.Errorf("empty agent spec")
	}
	parts := strings.SplitN(raw, ":", 2)
	spec := AgentSpec{Backend: strings.TrimSpace(parts[0])}
	if spec.Backend == "" {
		return AgentSpec{}, fmt.Errorf("missing backend in %q", raw)
	}
	if len(parts) == 2 {
		spec.Model = strings.TrimSpace(parts[1])
		if spec.Model == "" {
			return AgentSpec{}, fmt.Errorf("missing model in %q", raw)
		}
	}
	return spec, nil
}

func ParseAgentSpecs(raw string) ([]AgentSpec, error) {
	parts := strings.Split(raw, ",")
	out := make([]AgentSpec, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		spec, err := ParseAgentSpec(trimmed)
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func BuildFanoutWorkflow(parent *store.PendingJob, input FanoutInput) (store.Workflow, error) {
	if parent == nil {
		return store.Workflow{}, fmt.Errorf("missing parent job")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return store.Workflow{}, fmt.Errorf("missing workflow prompt")
	}
	if len(input.WorkerSpecs) == 0 {
		return store.Workflow{}, fmt.Errorf("fanout requires at least one worker backend")
	}
	if strings.TrimSpace(input.MergeSpec.Backend) == "" {
		return store.Workflow{}, fmt.Errorf("fanout requires a merge backend")
	}

	created := time.Now()
	wf := store.Workflow{
		ID:                store.NewWorkflowID(),
		ConversationID:    parent.ConversationID,
		ReplyConversation: parent.ReplyConversation,
		ParentJobID:       parent.ID,
		ParentThreadID:    parent.State.ThreadID,
		Pattern:           "fanout",
		Prompt:            strings.TrimSpace(input.Prompt),
		CWD:               parent.CWD,
		State:             parent.State,
		Notify:            strings.TrimSpace(input.Notify),
		DeliverMode:       "synthesize",
		Status:            "running",
		Created:           created,
		Updated:           created,
	}

	dependsOn := make([]string, 0, len(input.WorkerSpecs))
	for i, worker := range input.WorkerSpecs {
		stepID := fmt.Sprintf("worker-%d", i+1)
		dependsOn = append(dependsOn, stepID)
		wf.Steps = append(wf.Steps, store.WorkflowStep{
			ID:      stepID,
			Role:    "worker",
			Backend: worker.Backend,
			Model:   worker.Model,
			Prompt:  buildWorkerPrompt(wf.Prompt, i+1, len(input.WorkerSpecs)),
			Status:  "pending",
		})
	}
	wf.FinalStepID = "merge"
	wf.Steps = append(wf.Steps, store.WorkflowStep{
		ID:        "merge",
		Role:      "merge",
		Backend:   strings.TrimSpace(input.MergeSpec.Backend),
		Model:     strings.TrimSpace(input.MergeSpec.Model),
		Prompt:    "",
		DependsOn: dependsOn,
		Status:    "pending",
	})
	return wf, nil
}

func ReadyStepIDs(wf store.Workflow) []string {
	if isTerminalWorkflowStatus(wf.Status) {
		return nil
	}
	completed := make(map[string]bool, len(wf.Steps))
	for _, step := range wf.Steps {
		if step.Status == "completed" {
			completed[step.ID] = true
		}
	}
	var ready []string
	for _, step := range wf.Steps {
		if step.Status != "pending" {
			continue
		}
		depsReady := true
		for _, dep := range step.DependsOn {
			if !completed[dep] {
				depsReady = false
				break
			}
		}
		if depsReady {
			ready = append(ready, step.ID)
		}
	}
	return ready
}

func BuildMergePrompt(wf store.Workflow) string {
	var b strings.Builder
	b.WriteString("You are merging the results of a bounded fanout workflow into one final answer for the user.\n\n")
	b.WriteString("Original task:\n")
	b.WriteString(strings.TrimSpace(wf.Prompt))
	b.WriteString("\n\n")
	for _, step := range wf.Steps {
		if step.Role != "worker" || step.Status != "completed" {
			continue
		}
		b.WriteString("Worker ")
		b.WriteString(step.ID)
		if step.Backend != "" {
			b.WriteString(" (")
			b.WriteString(step.Backend)
			b.WriteString(")")
		}
		b.WriteString(":\n")
		b.WriteString(strings.TrimSpace(step.Result))
		b.WriteString("\n\n")
	}
	b.WriteString("Produce one concise final answer for the user. Preserve important disagreements if they matter.")
	return b.String()
}

func buildWorkerPrompt(task string, index, total int) string {
	return fmt.Sprintf("You are worker %d of %d in a bounded fanout workflow. Work independently and do not assume access to other worker results.\n\nTask:\n%s", index, total, strings.TrimSpace(task))
}

func isTerminalWorkflowStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "canceled":
		return true
	default:
		return false
	}
}
