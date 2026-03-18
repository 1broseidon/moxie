package bot

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
	tb "gopkg.in/telebot.v4"
)

type sendCall struct {
	to   string
	text string
	html bool
}

type fakeBot struct {
	sendCalls []sendCall
	sendErrs  []error
}

func (b *fakeBot) Send(to tb.Recipient, what interface{}, opts ...interface{}) (*tb.Message, error) {
	text, _ := what.(string)
	b.sendCalls = append(b.sendCalls, sendCall{
		to:   to.Recipient(),
		text: text,
		html: len(opts) > 0,
	})
	if len(b.sendErrs) > 0 {
		err := b.sendErrs[0]
		b.sendErrs = b.sendErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	return &tb.Message{ID: len(b.sendCalls)}, nil
}

func (b *fakeBot) Edit(msg tb.Editable, what interface{}, opts ...interface{}) (*tb.Message, error) {
	return &tb.Message{ID: 999}, nil
}

func (b *fakeBot) Delete(msg tb.Editable) error {
	return nil
}

func (b *fakeBot) Raw(method string, payload interface{}) ([]byte, error) {
	return nil, nil
}

func (b *fakeBot) FileByID(fileID string) (tb.File, error) {
	return tb.File{}, errors.New("not implemented")
}

func (b *fakeBot) File(file *tb.File) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func useBotStoreDir(t *testing.T) {
	t.Helper()
	restore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restore)
}

func TestParseCommand(t *testing.T) {
	base, arg := parseCommand("/model@rmkbl_bot claude sonnet")
	if base != "model" || arg != "claude sonnet" {
		t.Fatalf("parseCommand() = (%q, %q), want (%q, %q)", base, arg, "model", "claude sonnet")
	}

	base, arg = parseCommand("/new")
	if base != "new" || arg != "" {
		t.Fatalf("parseCommand(/new) = (%q, %q), want (new, empty)", base, arg)
	}
}

func TestRenderActivityHTML(t *testing.T) {
	got := renderActivityHTML("bash ls <weird>")
	if !strings.Contains(got, "<i>Running command…</i>") {
		t.Fatalf("renderActivityHTML() missing running summary: %q", got)
	}
	if !strings.Contains(got, "<code>ls &lt;weird&gt;</code>") {
		t.Fatalf("renderActivityHTML() missing escaped detail: %q", got)
	}

	got = renderActivityHTML("")
	if got != "<i>Working…</i>" {
		t.Fatalf("renderActivityHTML(empty) = %q, want working", got)
	}
}

func TestSplitResponseFiles(t *testing.T) {
	paths, cleaned := SplitResponseFiles("hello <send>/tmp/a.txt</send>\n<send>/tmp/b.png</send> world")
	if !reflect.DeepEqual(paths, []string{"/tmp/a.txt", "/tmp/b.png"}) {
		t.Fatalf("SplitResponseFiles() paths = %v", paths)
	}
	if strings.Join(strings.Fields(cleaned), " ") != "hello world" {
		t.Fatalf("SplitResponseFiles() cleaned = %q", cleaned)
	}
}

func TestSendChunkedFallsBackToPlainTextOnParseError(t *testing.T) {
	bot := &fakeBot{
		sendErrs: []error{
			errors.New("can't parse entities"),
			nil,
		},
	}

	if err := SendChunked(bot, 123, "bad <oops>tag</oops>"); err != nil {
		t.Fatalf("SendChunked() err = %v, want nil", err)
	}
	if len(bot.sendCalls) != 2 {
		t.Fatalf("send calls = %d, want 2", len(bot.sendCalls))
	}
	if bot.sendCalls[0].text != "bad <oops>tag</oops>" || !bot.sendCalls[0].html {
		t.Fatalf("first send call = %+v", bot.sendCalls[0])
	}
	if bot.sendCalls[1].text != "bad tag" || bot.sendCalls[1].html {
		t.Fatalf("fallback send call = %+v", bot.sendCalls[1])
	}
}

func TestSendChunkedReturnsErrorWhenNothingSends(t *testing.T) {
	bot := &fakeBot{
		sendErrs: []error{
			errors.New("telegram unavailable"),
		},
	}

	if err := SendChunked(bot, 123, "hello"); err == nil {
		t.Fatal("expected SendChunked to return error when nothing sends")
	}
}

func TestSendImmediateRetriesAfterDeliveryFailure(t *testing.T) {
	useBotStoreDir(t)

	bot := &fakeBot{
		sendErrs: []error{
			errors.New("temporary telegram failure"),
			nil,
		},
	}

	jobID, delivered := SendImmediate(bot, 123, "hello")
	if delivered {
		t.Fatal("expected initial delivery to fail and queue retry")
	}
	if jobID >= 0 {
		t.Fatalf("jobID = %d, want synthetic negative ID", jobID)
	}
	if !store.JobExists(jobID) {
		t.Fatalf("expected queued job %d to exist", jobID)
	}

	jobs := store.ListJobs()
	if len(jobs) != 1 || jobs[0].Status != "ready" || jobs[0].Result != "hello" {
		t.Fatalf("queued jobs = %+v, want one ready job with result", jobs)
	}

	if !RetryDeliverableJobs(bot, nil, nil) {
		t.Fatal("expected RetryDeliverableJobs to process queued message")
	}
	if store.JobExists(jobID) {
		t.Fatalf("expected retried job %d to be removed", jobID)
	}
	if len(bot.sendCalls) != 2 {
		t.Fatalf("send calls = %d, want 2", len(bot.sendCalls))
	}
}

func TestHandleNewSwitchesBackendAndWorkspace(t *testing.T) {
	useBotStoreDir(t)

	workspace := t.TempDir()
	cfg := &store.Config{
		Workspaces: map[string]string{"tele": workspace},
	}
	client := &oneagent.Client{
		Backends: map[string]oneagent.Backend{
			"claude": {DefaultModel: "sonnet"},
			"pi":     {},
		},
	}

	reply := handleNew("pi tele", store.State{Backend: "claude", ThreadID: "telegram"}, client, cfg)
	if !strings.Contains(reply, "New pi session in "+workspace+".") {
		t.Fatalf("handleNew() reply = %q", reply)
	}

	st := store.ReadState()
	if st.Backend != "pi" {
		t.Fatalf("backend = %q, want pi", st.Backend)
	}
	if st.CWD != workspace {
		t.Fatalf("cwd = %q, want %q", st.CWD, workspace)
	}
	if !strings.HasPrefix(st.ThreadID, "tg-") {
		t.Fatalf("thread id = %q, want tg- prefix", st.ThreadID)
	}
}
