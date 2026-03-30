package store

import (
	"strings"
	"testing"
	"time"
)

func TestWorkflowWriteReadRoundtrip(t *testing.T) {
	cleanup := SetConfigDir(t.TempDir())
	defer cleanup()

	wf := Workflow{
		ID:             NewWorkflowID(),
		ConversationID: "telegram:123",
		Pattern:        "fanout",
		Prompt:         "Research orchestration patterns",
		Status:         "running",
		Created:        time.Now().Add(-1 * time.Minute),
		Updated:        time.Now(),
		Steps: []WorkflowStep{
			{ID: "worker-1", Role: "worker", Backend: "codex", Status: "completed", Result: "worker output"},
			{ID: "merge", Role: "merge", Backend: "claude", Status: "pending", DependsOn: []string{"worker-1"}},
		},
	}
	if err := WriteWorkflow(wf); err != nil {
		t.Fatalf("WriteWorkflow() err = %v", err)
	}

	got, ok := ReadWorkflow(wf.ID)
	if !ok {
		t.Fatal("workflow not found after write")
	}
	if got.Pattern != wf.Pattern || got.Prompt != wf.Prompt || len(got.Steps) != len(wf.Steps) {
		t.Fatalf("ReadWorkflow() = %+v, want %+v", got, wf)
	}
}

func TestListWorkflowsSortedNewestFirst(t *testing.T) {
	cleanup := SetConfigDir(t.TempDir())
	defer cleanup()

	for i, name := range []string{"first", "second", "third"} {
		wf := Workflow{
			ID:             NewWorkflowID() + "-" + name,
			ConversationID: "telegram:123",
			Pattern:        "fanout",
			Prompt:         name,
			Status:         "running",
			Created:        time.Now().Add(time.Duration(i) * time.Second),
			Updated:        time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := WriteWorkflow(wf); err != nil {
			t.Fatalf("WriteWorkflow(%s) err = %v", name, err)
		}
	}

	got := ListWorkflows()
	if len(got) != 3 {
		t.Fatalf("ListWorkflows() len = %d, want 3", len(got))
	}
	if !strings.Contains(got[0].Prompt, "third") {
		t.Fatalf("newest workflow = %q, want third", got[0].Prompt)
	}
	if !strings.Contains(got[2].Prompt, "first") {
		t.Fatalf("oldest workflow = %q, want first", got[2].Prompt)
	}
}

func TestWorkflowEventsAppendAndRead(t *testing.T) {
	cleanup := SetConfigDir(t.TempDir())
	defer cleanup()

	workflowID := "wf-test"
	events := []WorkflowEvent{
		{Type: "workflow.created", StepID: "", Message: "created", TS: time.Now().Add(-2 * time.Second)},
		{Type: "step.completed", StepID: "worker-1", Message: "done", TS: time.Now().Add(-1 * time.Second)},
	}
	for _, ev := range events {
		if err := AppendWorkflowEvent(workflowID, ev); err != nil {
			t.Fatalf("AppendWorkflowEvent() err = %v", err)
		}
	}

	got, err := ReadWorkflowEvents(workflowID)
	if err != nil {
		t.Fatalf("ReadWorkflowEvents() err = %v", err)
	}
	if len(got) != len(events) {
		t.Fatalf("ReadWorkflowEvents() len = %d, want %d", len(got), len(events))
	}
	for i := range events {
		if got[i].Type != events[i].Type || got[i].StepID != events[i].StepID || got[i].Message != events[i].Message {
			t.Fatalf("event[%d] = %+v, want %+v", i, got[i], events[i])
		}
	}
}
