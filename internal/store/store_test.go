package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func useTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	restore := SetConfigDir(dir)
	t.Cleanup(restore)
	return dir
}

func TestLoadConfigValidationAndDefaults(t *testing.T) {
	dir := useTempConfigDir(t)

	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected missing config error")
	}

	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(); err == nil || err.Error() != "config missing at least one valid channel\nRun: moxie init" {
		t.Fatalf("LoadConfig() empty config err = %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"channels":{"slack":{"provider":"slack","app_token":"xapp-123"}}}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(); err == nil || err.Error() != "config missing at least one valid channel\nRun: moxie init" {
		t.Fatalf("LoadConfig() partial slack config err = %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"channels":{"slack":{"token":"xoxb-123","app_token":"xapp-123"}}}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() slack-only config: %v", err)
	}
	slack, err := cfg.Slack()
	if err != nil {
		t.Fatalf("Slack() = %v", err)
	}
	if slack.Provider != "slack" || slack.Token != "xoxb-123" || slack.AppToken != "xapp-123" {
		t.Fatalf("LoadConfig() slack = %+v, want token/app_token preserved", slack)
	}
	if cfg.Workspaces == nil {
		t.Fatal("expected workspaces map to be initialized")
	}

	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"token":"abc","chat_id":123}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err = LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() legacy telegram config: %v", err)
	}
	tg, err := cfg.Telegram()
	if err != nil {
		t.Fatalf("Telegram() = %v", err)
	}
	if tg.Provider != "telegram" || tg.Token != "abc" || tg.ChannelID != "123" {
		t.Fatalf("LoadConfig() telegram legacy = %+v, want token/channel preserved", tg)
	}

	SaveConfig(Config{
		Channels: map[string]ChannelConfig{
			"telegram": {
				Provider:  "telegram",
				Token:     "abc",
				ChannelID: "123",
			},
		},
		DefaultCWD:              "/tmp/workspace",
		SubagentMaxAttempts:     4,
		SubagentStallTimeout:    "6m",
		SubagentProgressTimeout: "90s",
		SubagentRetryBackoff:    []string{"0s", "45s", "3m"},
	})
	cfg, err = LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() round trip: %v", err)
	}
	tg, err = cfg.Telegram()
	if err != nil {
		t.Fatalf("Telegram() = %v", err)
	}
	if tg.Token != "abc" || tg.ChannelID != "123" {
		t.Fatalf("LoadConfig() telegram = %+v, want token/channel preserved", tg)
	}
	if cfg.DefaultCWD != "/tmp/workspace" {
		t.Fatalf("LoadConfig() default_cwd = %q, want /tmp/workspace", cfg.DefaultCWD)
	}
	if cfg.MaxSubagentAttempts() != 4 {
		t.Fatalf("MaxSubagentAttempts() = %d, want 4", cfg.MaxSubagentAttempts())
	}
	if cfg.SubagentStallDuration() != 6*time.Minute {
		t.Fatalf("SubagentStallDuration() = %v, want 6m", cfg.SubagentStallDuration())
	}
	if cfg.SubagentProgressDuration() != 90*time.Second {
		t.Fatalf("SubagentProgressDuration() = %v, want 90s", cfg.SubagentProgressDuration())
	}
	gotBackoff := cfg.SubagentRetryBackoffDurations()
	wantBackoff := []time.Duration{0, 45 * time.Second, 3 * time.Minute}
	if len(gotBackoff) != len(wantBackoff) {
		t.Fatalf("SubagentRetryBackoffDurations() len = %d, want %d", len(gotBackoff), len(wantBackoff))
	}
	for i := range wantBackoff {
		if gotBackoff[i] != wantBackoff[i] {
			t.Fatalf("SubagentRetryBackoffDurations()[%d] = %v, want %v", i, gotBackoff[i], wantBackoff[i])
		}
	}
}

func TestSubagentSupervisionDefaultsAndInvalidValues(t *testing.T) {
	cfg := Config{}

	if cfg.MaxSubagentDepth() != defaultSubagentMaxDepth {
		t.Fatalf("MaxSubagentDepth() = %d, want %d", cfg.MaxSubagentDepth(), defaultSubagentMaxDepth)
	}
	if cfg.MaxSubagentAttempts() != defaultSubagentMaxAttempts {
		t.Fatalf("MaxSubagentAttempts() = %d, want %d", cfg.MaxSubagentAttempts(), defaultSubagentMaxAttempts)
	}
	if cfg.SubagentStallDuration() != defaultSubagentStallTimeout {
		t.Fatalf("SubagentStallDuration() = %v, want %v", cfg.SubagentStallDuration(), defaultSubagentStallTimeout)
	}
	if cfg.SubagentProgressDuration() != defaultSubagentProgressTimeout {
		t.Fatalf("SubagentProgressDuration() = %v, want %v", cfg.SubagentProgressDuration(), defaultSubagentProgressTimeout)
	}
	gotBackoff := cfg.SubagentRetryBackoffDurations()
	if len(gotBackoff) != len(defaultSubagentRetryBackoff) {
		t.Fatalf("SubagentRetryBackoffDurations() len = %d, want %d", len(gotBackoff), len(defaultSubagentRetryBackoff))
	}
	for i := range defaultSubagentRetryBackoff {
		if gotBackoff[i] != defaultSubagentRetryBackoff[i] {
			t.Fatalf("SubagentRetryBackoffDurations()[%d] = %v, want %v", i, gotBackoff[i], defaultSubagentRetryBackoff[i])
		}
	}

	cfg = Config{
		SubagentMaxAttempts:     -1,
		SubagentStallTimeout:    "bad",
		SubagentProgressTimeout: "bad",
		SubagentRetryBackoff:    []string{"", "bad", "-1s"},
	}
	if cfg.MaxSubagentAttempts() != defaultSubagentMaxAttempts {
		t.Fatalf("MaxSubagentAttempts() invalid fallback = %d, want %d", cfg.MaxSubagentAttempts(), defaultSubagentMaxAttempts)
	}
	if cfg.SubagentStallDuration() != defaultSubagentStallTimeout {
		t.Fatalf("SubagentStallDuration() invalid fallback = %v, want %v", cfg.SubagentStallDuration(), defaultSubagentStallTimeout)
	}
	if cfg.SubagentProgressDuration() != defaultSubagentProgressTimeout {
		t.Fatalf("SubagentProgressDuration() invalid fallback = %v, want %v", cfg.SubagentProgressDuration(), defaultSubagentProgressTimeout)
	}

	cfg.SubagentProgressTimeout = "0s"
	if cfg.SubagentProgressDuration() != 0 {
		t.Fatalf("SubagentProgressDuration() disable = %v, want 0", cfg.SubagentProgressDuration())
	}

	gotBackoff = cfg.SubagentRetryBackoffDurations()
	if len(gotBackoff) != len(defaultSubagentRetryBackoff) {
		t.Fatalf("SubagentRetryBackoffDurations() invalid fallback len = %d, want %d", len(gotBackoff), len(defaultSubagentRetryBackoff))
	}
	for i := range defaultSubagentRetryBackoff {
		if gotBackoff[i] != defaultSubagentRetryBackoff[i] {
			t.Fatalf("SubagentRetryBackoffDurations() invalid fallback[%d] = %v, want %v", i, gotBackoff[i], defaultSubagentRetryBackoff[i])
		}
	}
}

func TestSlackValidation(t *testing.T) {
	cfg := Config{
		Channels: map[string]ChannelConfig{
			"slack": {},
		},
	}

	if _, err := cfg.Slack(); err == nil || err.Error() != "config missing slack token" {
		t.Fatalf("Slack() missing token err = %v", err)
	}

	cfg.Channels["slack"] = ChannelConfig{Token: "xoxb-123"}
	if _, err := cfg.Slack(); err == nil || err.Error() != "config missing slack app_token" {
		t.Fatalf("Slack() missing app_token err = %v", err)
	}

	slack, err := (Config{
		Channels: map[string]ChannelConfig{
			"slack": {Token: "xoxb-123", AppToken: "xapp-123"},
		},
	}).Slack()
	if err != nil {
		t.Fatalf("Slack() valid config: %v", err)
	}
	if slack.Provider != "slack" || slack.Token != "xoxb-123" || slack.AppToken != "xapp-123" {
		t.Fatalf("Slack() = %+v, want provider/token/app_token preserved", slack)
	}
}

func TestWebexValidation(t *testing.T) {
	cfg := Config{
		Channels: map[string]ChannelConfig{
			"webex": {},
		},
	}

	if _, err := cfg.Webex(); err == nil || err.Error() != "config missing webex token" {
		t.Fatalf("Webex() missing token err = %v", err)
	}

	webex, err := (Config{
		Channels: map[string]ChannelConfig{
			"webex": {Token: "bot-token", BotID: "bot-id", ChannelID: "room-id", AllowedUserIDs: []string{"user-1"}, AllowedEmails: []string{"user@example.com"}},
		},
	}).Webex()
	if err != nil {
		t.Fatalf("Webex() valid config: %v", err)
	}
	if webex.Provider != "webex" || webex.Token != "bot-token" || webex.BotID != "bot-id" || webex.ChannelID != "room-id" {
		t.Fatalf("Webex() = %+v, want provider/token/bot_id/channel_id preserved", webex)
	}
	if len(webex.AllowedUserIDs) != 1 || webex.AllowedUserIDs[0] != "user-1" {
		t.Fatalf("Webex() allowed_user_ids = %#v", webex.AllowedUserIDs)
	}
	if len(webex.AllowedEmails) != 1 || webex.AllowedEmails[0] != "user@example.com" {
		t.Fatalf("Webex() allowed_emails = %#v", webex.AllowedEmails)
	}
}

func TestReadWriteStateDefaultsAndRoundTrip(t *testing.T) {
	useTempConfigDir(t)

	got := ReadState()
	if got.Backend != "claude" || got.ThreadID != "chat" {
		t.Fatalf("ReadState() defaults = %+v, want backend=claude thread=chat", got)
	}

	want := State{
		Backend:  "pi",
		Model:    "small",
		ThreadID: "tg-123",
		CWD:      "/tmp/work",
	}
	WriteState(want)

	got = ReadState()
	if got != want {
		t.Fatalf("ReadState() = %+v, want %+v", got, want)
	}
}

func TestConversationStateRoundTripAndDefaultFallback(t *testing.T) {
	useTempConfigDir(t)

	WriteState(State{
		Backend:  "claude",
		ThreadID: "default-thread",
		CWD:      "/tmp/default",
	})

	tgConversation := "telegram:123"
	slackConversation := "slack:C123:1710000000.100"
	tgState := State{
		Backend:  "pi",
		Model:    "small",
		ThreadID: "tg-thread",
		CWD:      "/tmp/tg",
	}
	slackState := State{
		Backend:  "claude",
		Model:    "sonnet",
		ThreadID: "slack-thread",
		CWD:      "/tmp/slack",
	}

	WriteConversationState(tgConversation, tgState)
	WriteConversationState(slackConversation, slackState)

	if got := ReadConversationState(tgConversation); got != tgState {
		t.Fatalf("ReadConversationState(%q) = %+v, want %+v", tgConversation, got, tgState)
	}
	if got := ReadConversationState(slackConversation); got != slackState {
		t.Fatalf("ReadConversationState(%q) = %+v, want %+v", slackConversation, got, slackState)
	}

	got := ReadConversationState("telegram:other")
	want := State{
		Backend:  "claude",
		ThreadID: "default-thread",
		CWD:      "/tmp/default",
	}
	if got != want {
		t.Fatalf("ReadConversationState(fallback) = %+v, want %+v", got, want)
	}
}

func TestJobsRoundTripSortedAndSkipsCorruptEntries(t *testing.T) {
	dir := useTempConfigDir(t)

	WriteJob(PendingJob{ID: "job-20", Source: "telegram", SourceEventID: "20", Status: "ready", Result: "later"})
	WriteJob(PendingJob{ID: "job-10", Source: "telegram", SourceEventID: "10", Status: "ready", Result: "sooner"})
	if err := os.WriteFile(filepath.Join(dir, "jobs", "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write broken job: %v", err)
	}

	jobs := ListJobs()
	if len(jobs) != 2 {
		t.Fatalf("ListJobs() len = %d, want 2", len(jobs))
	}
	if jobs[0].ID != "job-10" || jobs[1].ID != "job-20" {
		t.Fatalf("ListJobs() order = %+v, want IDs [job-10 job-20]", []string{jobs[0].ID, jobs[1].ID})
	}
	if !JobExists("job-10") || !JobExists("job-20") {
		t.Fatal("expected written job files to exist")
	}

	RemoveJob("job-10")
	if JobExists("job-10") {
		t.Fatal("expected job job-10 to be removed")
	}
}

func TestReadJobRoundTripAndMissing(t *testing.T) {
	useTempConfigDir(t)

	want := PendingJob{
		ID:             "job-read",
		ConversationID: "telegram:123",
		Status:         "ready",
		Result:         "hello",
	}
	WriteJob(want)

	got, ok := ReadJob(want.ID)
	if !ok {
		t.Fatal("expected ReadJob to find persisted job")
	}
	if got.ID != want.ID || got.ConversationID != want.ConversationID || got.Status != want.Status || got.Result != want.Result {
		t.Fatalf("ReadJob() = %+v, want %+v", got, want)
	}

	if _, ok := ReadJob("missing-job"); ok {
		t.Fatal("expected ReadJob to report missing job")
	}
}

func TestPendingJobSupervisionRoundTrip(t *testing.T) {
	useTempConfigDir(t)

	now := time.Date(2026, 3, 22, 12, 0, 0, 0, time.UTC)
	want := PendingJob{
		ID:             "job-supervised",
		ConversationID: "telegram:123",
		Status:         "running",
		Supervision: SupervisionState{
			Attempt:        2,
			MaxAttempts:    3,
			ActiveRunID:    "run-123",
			LastEventAt:    now,
			LastProgressAt: now.Add(-15 * time.Second),
			LastError:      "stalled execution",
		},
	}
	WriteJob(want)

	got, ok := ReadJob(want.ID)
	if !ok {
		t.Fatal("expected ReadJob to find supervised job")
	}
	if got.Supervision != want.Supervision {
		t.Fatalf("ReadJob() supervision = %+v, want %+v", got.Supervision, want.Supervision)
	}
}

func TestCleanupJobTempRemovesFile(t *testing.T) {
	useTempConfigDir(t)

	tempFile := filepath.Join(t.TempDir(), "temp.txt")
	if err := os.WriteFile(tempFile, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	CleanupJobTemp(PendingJob{TempPath: tempFile})

	if _, err := os.Stat(tempFile); !os.IsNotExist(err) {
		t.Fatalf("expected temp file to be removed, stat err = %v", err)
	}
}
