package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/store"
	goslack "github.com/slack-go/slack"
)

func newSlackTestClient(t *testing.T, handler http.HandlerFunc) *goslack.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return goslack.New("test-token", goslack.OptionAPIURL(server.URL+"/"))
}

func slackOKResponse(t *testing.T, rw http.ResponseWriter, body map[string]any) {
	t.Helper()
	rw.Header().Set("Content-Type", "application/json")
	if body == nil {
		body = map[string]any{}
	}
	body["ok"] = true
	if err := json.NewEncoder(rw).Encode(body); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestSendPlainTextUsesThreadTimestamp(t *testing.T) {
	var form url.Values
	client := newSlackTestClient(t, func(rw http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/chat.postMessage" {
			t.Fatalf("unexpected path %s", req.URL.Path)
		}
		if err := req.ParseForm(); err != nil {
			t.Fatalf("ParseForm(): %v", err)
		}
		form = req.PostForm
		slackOKResponse(t, rw, map[string]any{"channel": "C123", "ts": "1710.1"})
	})

	err := SendPlainText(client, chat.ConversationRef{
		Provider:  chat.ProviderSlack,
		ChannelID: "C123",
		ThreadID:  "1700.1",
	}, "hello")
	if err != nil {
		t.Fatalf("SendPlainText(): %v", err)
	}
	if got := form.Get("thread_ts"); got != "1700.1" {
		t.Fatalf("thread_ts = %q, want 1700.1", got)
	}
	if got := form.Get("text"); got != "hello" {
		t.Fatalf("text = %q, want hello", got)
	}
}

func TestDeliverJobResultUsesStoredReplyThreadAndStripsSendTags(t *testing.T) {
	useSlackStoreDir(t)
	var form url.Values
	client := newSlackTestClient(t, func(rw http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			t.Fatalf("ParseForm(): %v", err)
		}
		form = req.PostForm
		slackOKResponse(t, rw, map[string]any{"channel": "C123", "ts": "1710.2"})
	})

	job := &store.PendingJob{
		ID:             "job-1",
		ConversationID: "slack:C123",
		Result:         "done\n<send>/tmp/report.txt</send>",
	}
	writeJobState(job.ID, jobState{
		ReplyConversation: chat.ConversationRef{Provider: chat.ProviderSlack, ChannelID: "C123", ThreadID: "1700.2"},
	})

	// DeliverJobResult without uploader — files logged as skipped, text delivered without send tags
	if err := DeliverJobResult(client, job); err != nil {
		t.Fatalf("DeliverJobResult(): %v", err)
	}
	if got := form.Get("thread_ts"); got != "1700.2" {
		t.Fatalf("thread_ts = %q, want 1700.2", got)
	}
	if got := form.Get("text"); got != "done" {
		t.Fatalf("text = %q, want 'done' (send tags stripped)", got)
	}
	if strings.Contains(form.Get("text"), "<send>") {
		t.Fatalf("text leaked send tag: %q", form.Get("text"))
	}
}

type fakeFileUploader struct {
	uploaded []string
}

func (f *fakeFileUploader) GetUploadURLExternalContext(_ context.Context, params goslack.GetUploadURLExternalParameters) (*goslack.GetUploadURLExternalResponse, error) {
	return &goslack.GetUploadURLExternalResponse{
		UploadURL: "https://fake-upload-url.example.com",
		FileID:    "F123",
	}, nil
}

func (f *fakeFileUploader) UploadToURL(_ context.Context, params goslack.UploadToURLParameters) error {
	f.uploaded = append(f.uploaded, params.Filename)
	return nil
}

func (f *fakeFileUploader) CompleteUploadExternalContext(_ context.Context, params goslack.CompleteUploadExternalParameters) (*goslack.CompleteUploadExternalResponse, error) {
	return &goslack.CompleteUploadExternalResponse{}, nil
}

func TestDeliverJobResultWithFilesUploads(t *testing.T) {
	useSlackStoreDir(t)

	var form url.Values
	client := newSlackTestClient(t, func(rw http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			t.Fatalf("ParseForm(): %v", err)
		}
		form = req.PostForm
		slackOKResponse(t, rw, map[string]any{"channel": "C123", "ts": "1710.2"})
	})

	// Create a temp file to upload
	tmpFile := t.TempDir() + "/report.txt"
	if err := os.WriteFile(tmpFile, []byte("report content"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	job := &store.PendingJob{
		ID:             "job-upload",
		ConversationID: "slack:C123",
		Result:         "done\n<send>" + tmpFile + "</send>",
	}
	writeJobState(job.ID, jobState{
		ReplyConversation: chat.ConversationRef{Provider: chat.ProviderSlack, ChannelID: "C123", ThreadID: "1700.2"},
	})

	uploader := &fakeFileUploader{}
	if err := DeliverJobResultWithFiles(client, uploader, job); err != nil {
		t.Fatalf("DeliverJobResultWithFiles(): %v", err)
	}
	if got := form.Get("text"); got != "done" {
		t.Fatalf("text = %q, want 'done'", got)
	}
	if len(uploader.uploaded) != 1 || uploader.uploaded[0] != "report.txt" {
		t.Fatalf("uploaded = %v, want [report.txt]", uploader.uploaded)
	}
}

func TestDeliverJobResultUsesInlineReplyConversation(t *testing.T) {
	useSlackStoreDir(t)

	var form url.Values
	client := newSlackTestClient(t, func(rw http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			t.Fatalf("ParseForm(): %v", err)
		}
		form = req.PostForm
		slackOKResponse(t, rw, map[string]any{"channel": "C123", "ts": "1710.8"})
	})

	job := &store.PendingJob{
		ID:                "job-inline",
		ConversationID:    "slack:C123",
		ReplyConversation: "slack:C123:1700.8",
		Result:            "done",
	}

	if err := DeliverJobResult(client, job); err != nil {
		t.Fatalf("DeliverJobResult(): %v", err)
	}
	if got := form.Get("thread_ts"); got != "1700.8" {
		t.Fatalf("thread_ts = %q, want 1700.8", got)
	}
}

func TestDeliverJobResultReportsEmptyBackendResponse(t *testing.T) {
	useSlackStoreDir(t)

	var form url.Values
	client := newSlackTestClient(t, func(rw http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			t.Fatalf("ParseForm(): %v", err)
		}
		form = req.PostForm
		slackOKResponse(t, rw, map[string]any{"channel": "C123", "ts": "1710.9"})
	})

	job := &store.PendingJob{
		ID:             "job-empty",
		ConversationID: "slack:C123",
		State: store.State{
			Backend: "pi",
		},
	}

	if err := DeliverJobResult(client, job); err != nil {
		t.Fatalf("DeliverJobResult(): %v", err)
	}
	if got := form.Get("text"); got != "Backend pi returned an empty response." {
		t.Fatalf("text = %q", got)
	}
}

func TestRunningStatusShowUpdateClear(t *testing.T) {
	useSlackStoreDir(t)

	var calls []string
	client := newSlackTestClient(t, func(rw http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			t.Fatalf("ParseForm(): %v", err)
		}
		calls = append(calls, req.URL.Path+":"+req.PostForm.Get("text"))
		switch req.URL.Path {
		case "/chat.postMessage":
			slackOKResponse(t, rw, map[string]any{"channel": "C123", "ts": "1710.3"})
		case "/chat.update":
			slackOKResponse(t, rw, map[string]any{"channel": "C123", "ts": "1710.3", "text": req.PostForm.Get("text")})
		case "/chat.delete":
			slackOKResponse(t, rw, map[string]any{"channel": "C123", "ts": "1710.3"})
		default:
			t.Fatalf("unexpected path %s", req.URL.Path)
		}
	})

	job := &store.PendingJob{ID: "job-2", ConversationID: "slack:C123"}
	writeJobState(job.ID, jobState{
		ReplyConversation: chat.ConversationRef{Provider: chat.ProviderSlack, ChannelID: "C123", ThreadID: "1700.3"},
	})

	status := newRunningStatus(client, job)
	status.show("read internal/slack/slack.go")
	status = newRunningStatus(client, job)
	status.show("write internal/slack/slack.go")
	status = newRunningStatus(client, job)
	status.clear()

	want := []string{
		"/chat.postMessage:Reading files...\nread internal/slack/slack.go",
		"/chat.update:Writing files...\nwrite internal/slack/slack.go",
		"/chat.delete:",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestSlackDispatchCallbacksShowsDelayedInitialStatus(t *testing.T) {
	useSlackStoreDir(t)

	var calls []string
	client := newSlackTestClient(t, func(rw http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			t.Fatalf("ParseForm(): %v", err)
		}
		calls = append(calls, req.URL.Path+":"+req.PostForm.Get("text"))
		switch req.URL.Path {
		case "/chat.postMessage":
			slackOKResponse(t, rw, map[string]any{"channel": "C123", "ts": "1710.7"})
		case "/chat.delete":
			slackOKResponse(t, rw, map[string]any{"channel": "C123", "ts": "1710.7"})
		default:
			t.Fatalf("unexpected path %s", req.URL.Path)
		}
	})

	job := &store.PendingJob{ID: "job-delayed", ConversationID: "slack:C123", Status: "running"}
	callbacks := slackDispatchCallbacks(client, job)
	time.Sleep(1400 * time.Millisecond)
	callbacks.OnDone()
	callbacks.OnStatusClear()

	if len(calls) == 0 {
		t.Fatal("expected delayed status message to be sent")
	}
	if !strings.Contains(calls[0], "/chat.postMessage:Working...") {
		t.Fatalf("first call = %q, want delayed Working status", calls[0])
	}
}
