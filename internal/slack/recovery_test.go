package slack

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/store"
)

func useRecoveryStoreDir(t *testing.T) {
	t.Helper()
	restore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restore)
}

func TestIsSlackJob(t *testing.T) {
	tests := []struct {
		job  store.PendingJob
		want bool
	}{
		{job: store.PendingJob{ConversationID: "slack:C1"}, want: true},
		{job: store.PendingJob{Source: string(chat.ProviderSlack)}, want: true},
		{job: store.PendingJob{Source: "subagent", ConversationID: "slack:C1"}, want: false},
		{job: store.PendingJob{Source: "subagent-synthesis", ConversationID: "slack:C1"}, want: true},
		{job: store.PendingJob{ConversationID: "telegram:1"}, want: false},
	}

	for _, tt := range tests {
		if got := isSlackJob(tt.job); got != tt.want {
			t.Fatalf("isSlackJob(%+v) = %v, want %v", tt.job, got, tt.want)
		}
	}
}

func TestRecoverPendingJobsProcessesOnlySlackJobs(t *testing.T) {
	useRecoveryStoreDir(t)

	store.WriteJob(store.PendingJob{ID: "job-slack", Status: "ready", ConversationID: "slack:C1", Result: "hello"})
	store.WriteJob(store.PendingJob{ID: "job-synth", Source: "subagent-synthesis", Status: "ready", ConversationID: "slack:C1", Result: "synth"})
	store.WriteJob(store.PendingJob{ID: "job-tg", Status: "ready", ConversationID: "telegram:1", Result: "ignore"})

	var seen []string
	client := newSlackTestClient(t, func(rw http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			t.Fatalf("ParseForm(): %v", err)
		}
		seen = append(seen, req.PostForm.Get("text"))
		slackOKResponse(t, rw, map[string]any{"channel": "C1", "ts": "1710.5"})
	})

	if !RecoverPendingJobs(client, nil, nil) {
		t.Fatal("expected slack recovery to report work")
	}
	if !reflect.DeepEqual(seen, []string{"hello", "synth"}) {
		t.Fatalf("seen = %v, want [hello synth]", seen)
	}
	if store.JobExists("job-slack") {
		t.Fatal("expected slack job to be removed")
	}
	if store.JobExists("job-synth") {
		t.Fatal("expected synthesis job to be removed")
	}
	if !store.JobExists("job-tg") {
		t.Fatal("expected telegram job to remain")
	}
}

func TestRetryDeliverableJobsProcessesOnlySlackJobs(t *testing.T) {
	useRecoveryStoreDir(t)

	store.WriteJob(store.PendingJob{ID: "job-slack", Status: "ready", ConversationID: "slack:C1", Result: "hello"})
	store.WriteJob(store.PendingJob{ID: "job-synth", Source: "subagent-synthesis", Status: "ready", ConversationID: "slack:C1", Result: "synth"})
	store.WriteJob(store.PendingJob{ID: "job-tg", Status: "ready", ConversationID: "telegram:1", Result: "ignore"})

	var seen []string
	client := newSlackTestClient(t, func(rw http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			t.Fatalf("ParseForm(): %v", err)
		}
		seen = append(seen, req.PostForm.Get("text"))
		slackOKResponse(t, rw, map[string]any{"channel": "C1", "ts": "1710.6"})
	})

	if !RetryDeliverableJobs(client, nil, nil) {
		t.Fatal("expected slack retry to report work")
	}
	if !reflect.DeepEqual(seen, []string{"hello", "synth"}) {
		t.Fatalf("seen = %v, want [hello synth]", seen)
	}
	if !store.JobExists("job-tg") {
		t.Fatal("expected telegram job to remain")
	}
}
