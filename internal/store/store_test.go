package store

import (
	"os"
	"path/filepath"
	"testing"
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

	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"chat_id":123}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(); err == nil || err.Error() != "config missing token\nRun: moxie init" {
		t.Fatalf("LoadConfig() missing token err = %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"token":"abc"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(); err == nil || err.Error() != "config missing chat_id\nRun: moxie init" {
		t.Fatalf("LoadConfig() missing chat_id err = %v", err)
	}

	SaveConfig(Config{Token: "abc", ChatID: 123})
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() round trip: %v", err)
	}
	if cfg.Token != "abc" || cfg.ChatID != 123 {
		t.Fatalf("LoadConfig() = %+v, want token/chat_id preserved", cfg)
	}
	if cfg.Workspaces == nil {
		t.Fatal("expected workspaces map to be initialized")
	}
}

func TestReadWriteStateDefaultsAndRoundTrip(t *testing.T) {
	useTempConfigDir(t)

	got := ReadState()
	if got.Backend != "claude" || got.ThreadID != "telegram" {
		t.Fatalf("ReadState() defaults = %+v, want backend=claude thread=telegram", got)
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

func TestJobsRoundTripSortedAndSkipsCorruptEntries(t *testing.T) {
	dir := useTempConfigDir(t)

	WriteJob(PendingJob{UpdateID: 20, Status: "ready", Result: "later"})
	WriteJob(PendingJob{UpdateID: 10, Status: "ready", Result: "sooner"})
	if err := os.WriteFile(filepath.Join(dir, "jobs", "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write broken job: %v", err)
	}

	jobs := ListJobs()
	if len(jobs) != 2 {
		t.Fatalf("ListJobs() len = %d, want 2", len(jobs))
	}
	if jobs[0].UpdateID != 10 || jobs[1].UpdateID != 20 {
		t.Fatalf("ListJobs() order = %+v, want IDs [10 20]", []int{jobs[0].UpdateID, jobs[1].UpdateID})
	}
	if !JobExists(10) || !JobExists(20) {
		t.Fatal("expected written job files to exist")
	}

	RemoveJob(10)
	if JobExists(10) {
		t.Fatal("expected job 10 to be removed")
	}
}

func TestCursorRoundTripAndCorruptFallback(t *testing.T) {
	dir := useTempConfigDir(t)

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

	if err := os.WriteFile(filepath.Join(dir, "cursor"), []byte("not-a-number"), 0o600); err != nil {
		t.Fatalf("write corrupt cursor: %v", err)
	}
	if got := ReadCursor(); got != 0 {
		t.Fatalf("ReadCursor() corrupt = %d, want 0", got)
	}
	if got := CursorOffset(); got != 0 {
		t.Fatalf("CursorOffset() corrupt = %d, want 0", got)
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
