package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type Workflow struct {
	ID                string         `json:"id"`
	ConversationID    string         `json:"conversation_id"`
	ReplyConversation string         `json:"reply_conversation,omitempty"`
	ParentJobID       string         `json:"parent_job_id,omitempty"`
	ParentThreadID    string         `json:"parent_thread_id,omitempty"`
	Pattern           string         `json:"pattern"`
	Prompt            string         `json:"prompt"`
	CWD               string         `json:"cwd,omitempty"`
	State             State          `json:"state,omitempty"`
	Notify            string         `json:"notify,omitempty"`
	DeliverMode       string         `json:"deliver_mode,omitempty"`
	Status            string         `json:"status"`
	Created           time.Time      `json:"created"`
	Updated           time.Time      `json:"updated"`
	Steps             []WorkflowStep `json:"steps,omitempty"`
	FinalStepID       string         `json:"final_step_id,omitempty"`
	FinalThreadID     string         `json:"final_thread_id,omitempty"`
	FinalArtifactID   string         `json:"final_artifact_id,omitempty"`
	LastError         string         `json:"last_error,omitempty"`
}

type WorkflowStep struct {
	ID             string    `json:"id"`
	Role           string    `json:"role"`
	Backend        string    `json:"backend"`
	Model          string    `json:"model,omitempty"`
	Prompt         string    `json:"prompt"`
	DependsOn      []string  `json:"depends_on,omitempty"`
	JobID          string    `json:"job_id,omitempty"`
	ThreadID       string    `json:"thread_id,omitempty"`
	ArtifactID     string    `json:"artifact_id,omitempty"`
	Status         string    `json:"status"`
	RetryCount     int       `json:"retry_count,omitempty"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
	LastProgressAt time.Time `json:"last_progress_at,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	Result         string    `json:"result,omitempty"`
}

type WorkflowEvent struct {
	Type    string    `json:"type"`
	StepID  string    `json:"step_id,omitempty"`
	Message string    `json:"message,omitempty"`
	TS      time.Time `json:"ts"`
}

func WorkflowsDir() string {
	dir := filepath.Join(ConfigDir(), "workflows")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

func WorkflowFile(id string) string {
	return filepath.Join(WorkflowsDir(), id+".json")
}

func workflowEventsFile(id string) string {
	return filepath.Join(WorkflowsDir(), id+".events.jsonl")
}

func WriteWorkflow(wf Workflow) error {
	if wf.ID == "" {
		wf.ID = NewWorkflowID()
	}
	if wf.Created.IsZero() {
		wf.Created = time.Now()
	}
	wf.Updated = time.Now()
	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workflow: %w", err)
	}
	return os.WriteFile(WorkflowFile(wf.ID), data, 0o600)
}

func ReadWorkflow(id string) (Workflow, bool) {
	data, err := os.ReadFile(WorkflowFile(id))
	if err != nil {
		return Workflow{}, false
	}
	var wf Workflow
	if err := json.Unmarshal(data, &wf); err != nil {
		return Workflow{}, false
	}
	return wf, true
}

func ListWorkflows() []Workflow {
	entries, err := os.ReadDir(WorkflowsDir())
	if err != nil {
		return nil
	}
	var workflows []Workflow
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" || filepath.Ext(trimSuffix(e.Name(), ".json")) == ".events" {
			continue
		}
		id := trimSuffix(e.Name(), ".json")
		if wf, ok := ReadWorkflow(id); ok {
			workflows = append(workflows, wf)
		}
	}
	sort.Slice(workflows, func(i, j int) bool {
		if workflows[i].Updated.Equal(workflows[j].Updated) {
			return workflows[i].ID > workflows[j].ID
		}
		return workflows[i].Updated.After(workflows[j].Updated)
	})
	return workflows
}

func AppendWorkflowEvent(id string, ev WorkflowEvent) error {
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	f, err := os.OpenFile(workflowEventsFile(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal workflow event: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func ReadWorkflowEvents(id string) ([]WorkflowEvent, error) {
	f, err := os.Open(workflowEventsFile(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var events []WorkflowEvent
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev WorkflowEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func NewWorkflowID() string {
	return fmt.Sprintf("wf-%d", time.Now().UnixNano())
}

func trimSuffix(s, suffix string) string {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}
