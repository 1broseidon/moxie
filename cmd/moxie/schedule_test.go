package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/1broseidon/moxie/internal/scheduler"
	"github.com/1broseidon/moxie/internal/store"
)

func TestResolveScheduleTrigger(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		at      string
		every   string
		cron    string
		want    scheduler.Trigger
		wantErr string
	}{
		{name: "in", in: "5m", want: scheduler.TriggerAt},
		{name: "at", at: "2026-03-18T10:00:00-05:00", want: scheduler.TriggerAt},
		{name: "every", every: "30m", want: scheduler.TriggerInterval},
		{name: "cron", cron: "0 1 * * *", want: scheduler.TriggerCalendar},
		{name: "missing", wantErr: "missing schedule trigger: use --in, --at, --every, or --cron"},
		{name: "multiple", in: "5m", every: "30m", wantErr: "use exactly one of --in, --at, --every, or --cron"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveScheduleTrigger(tt.in, tt.at, tt.every, tt.cron)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("resolveScheduleTrigger() err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveScheduleTrigger() err = %v", err)
			}
			if got != tt.want {
				t.Fatalf("resolveScheduleTrigger() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestFormatScheduleIntervalCompactsCanonicalDuration(t *testing.T) {
	if got := formatScheduleInterval("1h30m0s"); got != "1h30m" {
		t.Fatalf("formatScheduleInterval() = %q, want %q", got, "1h30m")
	}
	if got := formatScheduleInterval("2h0m0s"); got != "2h" {
		t.Fatalf("formatScheduleInterval() = %q, want %q", got, "2h")
	}
	if got := formatScheduleInterval("bad"); got != "bad" {
		t.Fatalf("formatScheduleInterval() = %q, want %q", got, "bad")
	}
}

func TestRenderScheduleIntervalUsesFriendlyDuration(t *testing.T) {
	prevLocal := time.Local
	loc := time.FixedZone("CDT", -5*60*60)
	time.Local = loc
	t.Cleanup(func() {
		time.Local = prevLocal
	})

	sc := scheduler.Schedule{
		ID:        "sch-interval",
		Action:    scheduler.ActionDispatch,
		Text:      "Check queue depth",
		CreatedAt: time.Date(2026, 3, 17, 21, 0, 0, 0, loc),
		NextRun:   time.Date(2026, 3, 17, 21, 30, 0, 0, loc),
		Spec: scheduler.ScheduleSpec{
			Trigger:  scheduler.TriggerInterval,
			Interval: "30m0s",
		},
		ConversationID: "telegram:123",
		Backend:        "claude",
		ThreadID:       "thread-1",
		Sync: scheduler.ScheduleSync{
			ManagedBy: scheduler.ManagedByInProcess,
			State:     scheduler.SyncStateFallback,
		},
	}

	headline := formatScheduleHeadline(sc)
	if !strings.Contains(headline, "every 30m next 2026-03-17 21:30 CDT") {
		t.Fatalf("formatScheduleHeadline() = %q", headline)
	}

	rendered := renderSchedule(sc)
	if !strings.Contains(rendered, "Trigger: every 30m") {
		t.Fatalf("renderSchedule() missing interval trigger: %q", rendered)
	}
	if strings.Contains(rendered, "30m0s") {
		t.Fatalf("renderSchedule() leaked canonical interval: %q", rendered)
	}
}

func writeScheduleFireTestConfig(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(store.ConfigDir(), 0700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	store.SaveConfig(store.Config{
		Channels: map[string]store.ChannelConfig{
			"telegram": {
				Provider:  "telegram",
				Token:     "tg-token",
				ChannelID: "123",
			},
			"slack": {
				Provider:  "slack",
				Token:     "xoxb-token",
				AppToken:  "xapp-token",
				ChannelID: "C123",
			},
		},
	})
}

func TestFireScheduleExecutionRunsTelegramSend(t *testing.T) {
	restoreStore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restoreStore)
	writeScheduleFireTestConfig(t)

	loc := time.FixedZone("CDT", -5*60*60)
	now := time.Date(2026, 3, 18, 10, 0, 0, 0, loc)
	schedules := scheduler.NewStore(filepath.Join(store.ConfigDir(), "schedules.json"), loc)
	sc, err := schedules.Add(scheduler.AddInput{
		Trigger:        scheduler.TriggerAt,
		Action:         scheduler.ActionSend,
		In:             "5m",
		Text:           "Call John",
		ConversationID: "telegram:123",
		Now:            now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}

	prevTelegram := prepareTelegramScheduleFire
	t.Cleanup(func() { prepareTelegramScheduleFire = prevTelegram })

	var (
		gotJob      store.PendingJob
		needsClient bool
	)
	prepareTelegramScheduleFire = func(cfg store.Config, schedules *scheduler.Store, requestedClient bool) (scheduleJobExecutor, error) {
		needsClient = requestedClient
		return func(job store.PendingJob) error {
			gotJob = job
			if _, err := schedules.MarkDone(job.ScheduleID, job.ID, now.Add(time.Second)); err != nil {
				return err
			}
			store.RemoveJob(job.ID)
			return nil
		}, nil
	}

	jobID, alreadyRunning, err := fireScheduleExecution(schedules, sc)
	if err != nil {
		t.Fatalf("fireScheduleExecution() err = %v", err)
	}
	if alreadyRunning {
		t.Fatal("fireScheduleExecution() reported already running")
	}
	if jobID == "" {
		t.Fatal("fireScheduleExecution() returned empty job id")
	}
	if needsClient {
		t.Fatal("telegram send should not request a backend client")
	}
	if gotJob.ScheduleID != sc.ID || gotJob.ConversationID != "telegram:123" {
		t.Fatalf("got job conversation/schedule = (%q, %q)", gotJob.ScheduleID, gotJob.ConversationID)
	}
	if gotJob.Status != "ready" || gotJob.Result != "Call John" || gotJob.Prompt != "" {
		t.Fatalf("got job = %+v", gotJob)
	}
	if _, err := schedules.Get(sc.ID); !os.IsNotExist(err) {
		t.Fatalf("schedule should be removed after one-shot fire, err = %v", err)
	}
	if store.JobExists(jobID) {
		t.Fatalf("job %s should be cleaned up", jobID)
	}
}

func TestFireScheduleExecutionRunsSlackDispatchWithStoredContext(t *testing.T) {
	restoreStore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restoreStore)
	writeScheduleFireTestConfig(t)

	loc := time.FixedZone("CDT", -5*60*60)
	now := time.Date(2026, 3, 18, 10, 0, 0, 0, loc)
	schedules := scheduler.NewStore(filepath.Join(store.ConfigDir(), "schedules.json"), loc)
	sc, err := schedules.Add(scheduler.AddInput{
		Trigger:        scheduler.TriggerAt,
		Action:         scheduler.ActionDispatch,
		In:             "10m",
		Text:           "Check deploy status",
		ConversationID: "slack:C123:1710000000.100",
		Backend:        "pi",
		Model:          "small",
		ThreadID:       "thread-9",
		CWD:            "/tmp/project",
		Now:            now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}

	prevSlack := prepareSlackScheduleFire
	t.Cleanup(func() { prepareSlackScheduleFire = prevSlack })

	var (
		gotJob      store.PendingJob
		needsClient bool
	)
	prepareSlackScheduleFire = func(cfg store.Config, schedules *scheduler.Store, requestedClient bool) (scheduleJobExecutor, error) {
		needsClient = requestedClient
		return func(job store.PendingJob) error {
			gotJob = job
			if _, err := schedules.MarkDone(job.ScheduleID, job.ID, now.Add(2*time.Second)); err != nil {
				return err
			}
			store.RemoveJob(job.ID)
			return nil
		}, nil
	}

	jobID, alreadyRunning, err := fireScheduleExecution(schedules, sc)
	if err != nil {
		t.Fatalf("fireScheduleExecution() err = %v", err)
	}
	if alreadyRunning {
		t.Fatal("fireScheduleExecution() reported already running")
	}
	if jobID == "" {
		t.Fatal("fireScheduleExecution() returned empty job id")
	}
	if !needsClient {
		t.Fatal("slack dispatch should request a backend client")
	}
	if gotJob.Prompt != "Check deploy status" {
		t.Fatalf("got prompt = %q", gotJob.Prompt)
	}
	if gotJob.ConversationID != "slack:C123:1710000000.100" {
		t.Fatalf("got conversation = %q", gotJob.ConversationID)
	}
	if gotJob.State.Backend != "pi" || gotJob.State.Model != "small" || gotJob.State.ThreadID != "thread-9" || gotJob.State.CWD != "/tmp/project" {
		t.Fatalf("got state = %+v", gotJob.State)
	}
}

func TestFireScheduleExecutionReturnsExistingRunningJob(t *testing.T) {
	restoreStore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restoreStore)

	loc := time.FixedZone("CDT", -5*60*60)
	now := time.Date(2026, 3, 18, 10, 0, 0, 0, loc)
	schedules := scheduler.NewStore(filepath.Join(store.ConfigDir(), "schedules.json"), loc)
	sc, err := schedules.Add(scheduler.AddInput{
		Trigger:        scheduler.TriggerAt,
		Action:         scheduler.ActionSend,
		In:             "5m",
		Text:           "Call John",
		ConversationID: "telegram:123",
		Now:            now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}

	store.WriteJob(store.PendingJob{ID: "job-existing", ScheduleID: sc.ID, ConversationID: "telegram:123", Status: "running"})
	if _, err := schedules.AttachJob(sc.ID, "job-existing"); err != nil {
		t.Fatalf("AttachJob(): %v", err)
	}
	locked, err := schedules.Get(sc.ID)
	if err != nil {
		t.Fatalf("Get(): %v", err)
	}

	prevTelegram := prepareTelegramScheduleFire
	t.Cleanup(func() { prepareTelegramScheduleFire = prevTelegram })
	prepareTelegramScheduleFire = func(store.Config, *scheduler.Store, bool) (scheduleJobExecutor, error) {
		t.Fatal("prepareTelegramScheduleFire should not be called for an already-running schedule")
		return nil, nil
	}

	jobID, alreadyRunning, err := fireScheduleExecution(schedules, locked)
	if err != nil {
		t.Fatalf("fireScheduleExecution() err = %v", err)
	}
	if !alreadyRunning {
		t.Fatal("fireScheduleExecution() should report already running")
	}
	if jobID != "job-existing" {
		t.Fatalf("job id = %q, want job-existing", jobID)
	}
}

func TestLoadScheduleForFireRepairsStaleRunningJob(t *testing.T) {
	restoreStore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restoreStore)

	loc := time.FixedZone("CDT", -5*60*60)
	now := time.Date(2026, 3, 18, 10, 0, 0, 0, loc)
	schedules := scheduler.NewStore(filepath.Join(store.ConfigDir(), "schedules.json"), loc)
	sc, err := schedules.Add(scheduler.AddInput{
		Trigger:        scheduler.TriggerAt,
		Action:         scheduler.ActionSend,
		In:             "5m",
		Text:           "Call John",
		ConversationID: "telegram:123",
		Now:            now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}
	if _, err := schedules.AttachJob(sc.ID, "job-stale"); err != nil {
		t.Fatalf("AttachJob(): %v", err)
	}

	repaired, err := loadScheduleForFire(schedules, sc.ID)
	if err != nil {
		t.Fatalf("loadScheduleForFire() err = %v", err)
	}
	if repaired.RunningJobID != "" {
		t.Fatalf("repaired running job = %q, want empty", repaired.RunningJobID)
	}
}
