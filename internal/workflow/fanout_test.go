package workflow

import (
	"reflect"
	"strings"
	"testing"

	"github.com/1broseidon/moxie/internal/store"
)

func TestParseAgentSpec(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    AgentSpec
		wantErr bool
	}{
		{name: "backend only", raw: "claude", want: AgentSpec{Backend: "claude"}},
		{name: "backend and model", raw: "claude:opus", want: AgentSpec{Backend: "claude", Model: "opus"}},
		{name: "trim spaces", raw: " pi : openai-codex/gpt-5.4 ", want: AgentSpec{Backend: "pi", Model: "openai-codex/gpt-5.4"}},
		{name: "missing backend", raw: ":opus", wantErr: true},
		{name: "missing model after colon", raw: "claude:", wantErr: true},
		{name: "empty", raw: "  ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAgentSpec(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseAgentSpec(%q) err = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Fatalf("ParseAgentSpec(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseAgentSpecs(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []AgentSpec
		wantErr bool
	}{
		{name: "comma separated", raw: "codex,claude,pi", want: []AgentSpec{{Backend: "codex"}, {Backend: "claude"}, {Backend: "pi"}}},
		{name: "with models", raw: "claude:opus,pi:openai-codex/gpt-5.4", want: []AgentSpec{{Backend: "claude", Model: "opus"}, {Backend: "pi", Model: "openai-codex/gpt-5.4"}}},
		{name: "trims spaces and empties", raw: " codex , , claude:sonnet ", want: []AgentSpec{{Backend: "codex"}, {Backend: "claude", Model: "sonnet"}}},
		{name: "empty", raw: "  ", want: nil},
		{name: "invalid entry", raw: "claude,pi:", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAgentSpecs(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseAgentSpecs(%q) err = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseAgentSpecs(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestBuildFanoutWorkflow(t *testing.T) {
	parent := &store.PendingJob{
		ID:                "job-parent",
		ConversationID:    "telegram:123",
		ReplyConversation: "telegram:123",
		CWD:               "/tmp/repo",
		State:             store.State{Backend: "claude", Model: "sonnet", ThreadID: "main-thread", Thinking: "medium"},
	}

	wf, err := BuildFanoutWorkflow(parent, FanoutInput{
		Prompt:      "Research agent orchestration",
		WorkerSpecs: []AgentSpec{{Backend: "codex", Model: "gpt-5.4"}, {Backend: "pi", Model: "openai-codex/gpt-5.4"}},
		MergeSpec:   AgentSpec{Backend: "claude", Model: "opus"},
	})
	if err != nil {
		t.Fatalf("BuildFanoutWorkflow() err = %v", err)
	}
	if wf.Pattern != "fanout" {
		t.Fatalf("Pattern = %q, want fanout", wf.Pattern)
	}
	if wf.ParentJobID != parent.ID {
		t.Fatalf("ParentJobID = %q, want %q", wf.ParentJobID, parent.ID)
	}
	if wf.CWD != parent.CWD {
		t.Fatalf("CWD = %q, want %q", wf.CWD, parent.CWD)
	}
	if wf.State != parent.State {
		t.Fatalf("State = %+v, want %+v", wf.State, parent.State)
	}
	if len(wf.Steps) != 3 {
		t.Fatalf("len(Steps) = %d, want 3", len(wf.Steps))
	}
	if wf.Steps[0].Role != "worker" || wf.Steps[1].Role != "worker" || wf.Steps[2].Role != "merge" {
		t.Fatalf("step roles = %+v", []string{wf.Steps[0].Role, wf.Steps[1].Role, wf.Steps[2].Role})
	}
	if wf.Steps[0].Backend != "codex" || wf.Steps[0].Model != "gpt-5.4" {
		t.Fatalf("worker-1 = %+v, want codex/gpt-5.4", wf.Steps[0])
	}
	if wf.Steps[1].Backend != "pi" || wf.Steps[1].Model != "openai-codex/gpt-5.4" {
		t.Fatalf("worker-2 = %+v, want pi/openai-codex/gpt-5.4", wf.Steps[1])
	}
	if wf.Steps[2].Backend != "claude" || wf.Steps[2].Model != "opus" {
		t.Fatalf("merge = %+v, want claude/opus", wf.Steps[2])
	}
	if !reflect.DeepEqual(wf.Steps[2].DependsOn, []string{"worker-1", "worker-2"}) {
		t.Fatalf("merge DependsOn = %v, want [worker-1 worker-2]", wf.Steps[2].DependsOn)
	}
}

func TestReadyStepIDs(t *testing.T) {
	tests := []struct {
		name string
		wf   store.Workflow
		want []string
	}{
		{
			name: "workers start immediately",
			wf:   store.Workflow{Status: "running", Steps: []store.WorkflowStep{{ID: "worker-1", Role: "worker", Status: "pending"}, {ID: "worker-2", Role: "worker", Status: "pending"}, {ID: "merge", Role: "merge", Status: "pending", DependsOn: []string{"worker-1", "worker-2"}}}},
			want: []string{"worker-1", "worker-2"},
		},
		{
			name: "merge waits for all workers",
			wf:   store.Workflow{Status: "running", Steps: []store.WorkflowStep{{ID: "worker-1", Role: "worker", Status: "completed"}, {ID: "worker-2", Role: "worker", Status: "completed"}, {ID: "merge", Role: "merge", Status: "pending", DependsOn: []string{"worker-1", "worker-2"}}}},
			want: []string{"merge"},
		},
		{
			name: "running step is not ready again",
			wf:   store.Workflow{Status: "running", Steps: []store.WorkflowStep{{ID: "worker-1", Role: "worker", Status: "running"}, {ID: "merge", Role: "merge", Status: "pending", DependsOn: []string{"worker-1"}}}},
			want: nil,
		},
		{
			name: "terminal workflow has no ready steps",
			wf:   store.Workflow{Status: "completed", Steps: []store.WorkflowStep{{ID: "worker-1", Role: "worker", Status: "pending"}}},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReadyStepIDs(tt.wf)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ReadyStepIDs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildMergePromptIncludesWorkerResults(t *testing.T) {
	wf := store.Workflow{
		Prompt: "Research orchestration patterns for Moxie",
		Steps: []store.WorkflowStep{
			{ID: "worker-1", Role: "worker", Backend: "codex", Status: "completed", Result: "result one"},
			{ID: "worker-2", Role: "worker", Backend: "pi", Status: "completed", Result: "result two"},
		},
	}

	got := BuildMergePrompt(wf)
	for _, want := range []string{"Research orchestration patterns for Moxie", "Worker worker-1 (codex)", "result one", "Worker worker-2 (pi)", "result two"} {
		if !strings.Contains(got, want) {
			t.Fatalf("BuildMergePrompt() missing %q in %q", want, got)
		}
	}
}
