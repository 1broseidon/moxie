package bot

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/1broseidon/moxie/internal/chat"
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

	conversation := chat.ConversationRef{Provider: chat.ProviderTelegram, ChannelID: "123"}
	if err := SendChunked(bot, conversation, "bad <oops>tag</oops>"); err != nil {
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

	conversation := chat.ConversationRef{Provider: chat.ProviderTelegram, ChannelID: "123"}
	if err := SendChunked(bot, conversation, "hello"); err == nil {
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

	conversation := chat.ConversationRef{Provider: chat.ProviderTelegram, ChannelID: "123"}
	jobID, delivered := SendImmediate(bot, conversation, "hello")
	if delivered {
		t.Fatal("expected initial delivery to fail and queue retry")
	}
	if !strings.HasPrefix(jobID, "job-") {
		t.Fatalf("jobID = %q, want job- prefix", jobID)
	}
	if !store.JobExists(jobID) {
		t.Fatalf("expected queued job %s to exist", jobID)
	}

	jobs := store.ListJobs()
	if len(jobs) != 1 || jobs[0].Status != "ready" || jobs[0].Result != "hello" {
		t.Fatalf("queued jobs = %+v, want one ready job with result", jobs)
	}

	if !RetryDeliverableJobs(bot, nil, nil) {
		t.Fatal("expected RetryDeliverableJobs to process queued message")
	}
	if store.JobExists(jobID) {
		t.Fatalf("expected retried job %s to be removed", jobID)
	}
	if len(bot.sendCalls) != 2 {
		t.Fatalf("send calls = %d, want 2", len(bot.sendCalls))
	}
}

func TestCursorRoundTripAndCorruptFallback(t *testing.T) {
	useBotStoreDir(t)

	if got := ReadCursor(); got != 0 {
		t.Fatalf("ReadCursor() missing = %d, want 0", got)
	}

	WriteCursor(42)
	if got := ReadCursor(); got != 42 {
		t.Fatalf("ReadCursor() = %d, want 42", got)
	}
	if got := CursorOffset(); got != 43 {
		t.Fatalf("CursorOffset() = %d, want 43", got)
	}

	if err := os.WriteFile(filepath.Join(store.ConfigDir(), "telegram-cursor"), []byte("not-a-number"), 0o600); err != nil {
		t.Fatalf("write corrupt cursor: %v", err)
	}
	if got := ReadCursor(); got != 0 {
		t.Fatalf("ReadCursor() corrupt = %d, want 0", got)
	}
}

func TestApplySystemPrompt(t *testing.T) {
	backends := map[string]oneagent.Backend{
		"claude": {SystemPrompt: "base"},
		"pi":     {},
	}

	ApplySystemPrompt(backends)

	if !strings.Contains(backends["claude"].SystemPrompt, TelegramSystemPrompt) {
		t.Fatalf("claude prompt missing telegram system prompt: %q", backends["claude"].SystemPrompt)
	}
	if backends["pi"].SystemPrompt != TelegramSystemPrompt {
		t.Fatalf("pi prompt = %q, want TelegramSystemPrompt", backends["pi"].SystemPrompt)
	}
}
