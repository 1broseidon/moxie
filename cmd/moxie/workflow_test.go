package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/1broseidon/moxie/internal/store"
	workflowpkg "github.com/1broseidon/moxie/internal/workflow"
)

func TestParseWorkflowRunArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    workflowRunArgs
		wantErr bool
	}{
		{
			name: "fanout happy path",
			args: []string{"fanout", "--workers", "codex,claude", "--merge", "claude", "--text", "Research orchestration"},
			want: workflowRunArgs{pattern: "fanout", workers: []workflowpkg.AgentSpec{{Backend: "codex"}, {Backend: "claude"}}, merge: workflowpkg.AgentSpec{Backend: "claude"}, text: "Research orchestration", notify: "silent"},
		},
		{
			name: "fanout worker and merge models",
			args: []string{"fanout", "--workers", "claude:opus,pi:openai-codex/gpt-5.4", "--merge", "claude:sonnet", "--text", "Compare outputs"},
			want: workflowRunArgs{pattern: "fanout", workers: []workflowpkg.AgentSpec{{Backend: "claude", Model: "opus"}, {Backend: "pi", Model: "openai-codex/gpt-5.4"}}, merge: workflowpkg.AgentSpec{Backend: "claude", Model: "sonnet"}, text: "Compare outputs", notify: "silent"},
		},
		{
			name:    "missing pattern",
			args:    []string{},
			wantErr: true,
		},
		{
			name:    "unsupported pattern",
			args:    []string{"judge", "--workers", "codex", "--merge", "claude", "--text", "pick a winner"},
			wantErr: true,
		},
		{
			name:    "missing workers",
			args:    []string{"fanout", "--merge", "claude", "--text", "Research orchestration"},
			wantErr: true,
		},
		{
			name:    "missing merge",
			args:    []string{"fanout", "--workers", "codex", "--text", "Research orchestration"},
			wantErr: true,
		},
		{
			name:    "missing text",
			args:    []string{"fanout", "--workers", "codex", "--merge", "claude"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseWorkflowRunArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseWorkflowRunArgs() err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseWorkflowRunArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPortableWorkflowThinking(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: ""},
		{name: "off disables", raw: "off", want: ""},
		{name: "low", raw: "low", want: "low"},
		{name: "medium", raw: " medium ", want: "medium"},
		{name: "high upper", raw: "HIGH", want: "high"},
		{name: "max", raw: "max", want: "max"},
		{name: "non portable dropped", raw: "xhigh", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := portableWorkflowThinking(tt.raw)
			if got != tt.want {
				t.Fatalf("portableWorkflowThinking(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestRecordWorkflowStepActivity(t *testing.T) {
	tests := []struct {
		name             string
		activity         string
		lastProgressAt   time.Time
		wantEventCount   int
		wantEventMessage string
		wantProgress     bool
	}{
		{
			name:             "stores compact progress event",
			activity:         "  read   /tmp/file\n  next ",
			lastProgressAt:   time.Date(2026, 3, 30, 6, 0, 0, 0, time.UTC),
			wantEventCount:   1,
			wantEventMessage: "read /tmp/file next",
			wantProgress:     true,
		},
		{
			name:           "ignores empty activity",
			activity:       "   \n\t  ",
			lastProgressAt: time.Date(2026, 3, 30, 6, 0, 1, 0, time.UTC),
			wantEventCount: 0,
			wantProgress:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := store.SetConfigDir(t.TempDir())
			defer cleanup()

			wf := store.Workflow{
				ID:             "wf-test",
				ConversationID: "telegram:123",
				Pattern:        "fanout",
				Prompt:         "test prompt",
				Status:         "running",
				Created:        time.Now().Add(-1 * time.Minute),
				Updated:        time.Now().Add(-1 * time.Minute),
				Steps: []store.WorkflowStep{{
					ID:     "worker-1",
					Role:   "worker",
					Status: "running",
				}},
			}
			if err := store.WriteWorkflow(wf); err != nil {
				t.Fatalf("WriteWorkflow() err = %v", err)
			}

			st := &workflowTransports{}
			job := &store.PendingJob{Supervision: store.SupervisionState{LastProgressAt: tt.lastProgressAt}}
			recordWorkflowStepActivity(st, wf.ID, "worker-1", job, tt.activity)

			gotWF, ok := store.ReadWorkflow(wf.ID)
			if !ok {
				t.Fatal("workflow missing after activity write")
			}
			events, err := store.ReadWorkflowEvents(wf.ID)
			if err != nil {
				t.Fatalf("ReadWorkflowEvents() err = %v", err)
			}
			if len(events) != tt.wantEventCount {
				t.Fatalf("event count = %d, want %d", len(events), tt.wantEventCount)
			}
			if tt.wantEventCount > 0 {
				if events[0].Type != "step.progress" {
					t.Fatalf("event type = %q, want step.progress", events[0].Type)
				}
				if events[0].Message != tt.wantEventMessage {
					t.Fatalf("event message = %q, want %q", events[0].Message, tt.wantEventMessage)
				}
			}
			gotProgress := !gotWF.Steps[0].LastProgressAt.IsZero()
			if gotProgress != tt.wantProgress {
				t.Fatalf("has LastProgressAt = %v, want %v", gotProgress, tt.wantProgress)
			}
			if tt.wantProgress && !gotWF.Steps[0].LastProgressAt.Equal(tt.lastProgressAt) {
				t.Fatalf("LastProgressAt = %v, want %v", gotWF.Steps[0].LastProgressAt, tt.lastProgressAt)
			}
		})
	}
}

func TestWorkflowFailureResultFeelsLikeSubagentFailure(t *testing.T) {
	tests := []struct {
		name           string
		wf             store.Workflow
		step           store.WorkflowStep
		mustContain    []string
		mustNotContain []string
	}{
		{
			name: "worker failure is user facing without workflow jargon",
			wf:   store.Workflow{Pattern: "fanout", Prompt: "Research orchestration"},
			step: store.WorkflowStep{ID: "worker-1", Backend: "claude", LastError: "backend unavailable"},
			mustContain: []string{
				"Subagent failed while gathering results.",
				"Backend: claude",
				"Task: Research orchestration",
				"Last observed error: backend unavailable",
			},
			mustNotContain: []string{"Workflow failed", "Pattern:"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workflowFailureResult(tt.wf, tt.step)
			for _, want := range tt.mustContain {
				if !strings.Contains(got, want) {
					t.Fatalf("workflowFailureResult() missing %q in %q", want, got)
				}
			}
			for _, banned := range tt.mustNotContain {
				if strings.Contains(got, banned) {
					t.Fatalf("workflowFailureResult() unexpectedly contained %q in %q", banned, got)
				}
			}
		})
	}
}
