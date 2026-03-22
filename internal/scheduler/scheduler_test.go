package scheduler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	loc := time.FixedZone("CDT", -5*60*60)
	return NewStore(filepath.Join(t.TempDir(), "schedules.json"), loc)
}

func TestAddAtSchedule(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.Add(AddInput{
		Trigger: TriggerAt,
		Action:  ActionSend,
		At:      "2026-03-18T10:00:00-05:00",
		Text:    "Call John",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("add schedule: %v", err)
	}

	if sc.Trigger != TriggerAt {
		t.Fatalf("trigger = %s, want %s", sc.Trigger, TriggerAt)
	}
	if !sc.NextRun.Equal(sc.At) {
		t.Fatalf("next run = %v, want %v", sc.NextRun, sc.At)
	}
}

func TestAddRelativeSchedule(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.Add(AddInput{
		Trigger: TriggerAt,
		Action:  ActionSend,
		In:      "1d2h30m",
		Text:    "Relative reminder",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("add schedule: %v", err)
	}

	want := now.Add(26*time.Hour + 30*time.Minute)
	if !sc.At.Equal(want) {
		t.Fatalf("at = %v, want %v", sc.At, want)
	}
	if !sc.NextRun.Equal(want) {
		t.Fatalf("next run = %v, want %v", sc.NextRun, want)
	}
}

func TestAddIntervalSchedule(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.Add(AddInput{
		Trigger: TriggerInterval,
		Action:  ActionDispatch,
		Every:   "90m",
		Text:    "Run cleanup",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("add schedule: %v", err)
	}

	if sc.Trigger != TriggerInterval {
		t.Fatalf("trigger = %s, want %s", sc.Trigger, TriggerInterval)
	}
	if sc.Interval != "1h30m0s" {
		t.Fatalf("interval = %q, want %q", sc.Interval, "1h30m0s")
	}
	if !sc.NextRun.Equal(now.Add(90 * time.Minute)) {
		t.Fatalf("next run = %v, want %v", sc.NextRun, now.Add(90*time.Minute))
	}
	if sc.ManagedBy != ManagedByInProcess {
		t.Fatalf("managed_by = %q, want %q", sc.ManagedBy, ManagedByInProcess)
	}
	if sc.SyncState != SyncStateFallback {
		t.Fatalf("sync_state = %q, want %q", sc.SyncState, SyncStateFallback)
	}
}

func TestMarkDoneRemovesOneShot(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.Add(AddInput{
		Trigger: TriggerAt,
		Action:  ActionSend,
		At:      "2026-03-18T10:00:00-05:00",
		Text:    "Call John",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("add schedule: %v", err)
	}
	if _, err := store.AttachJob(sc.ID, "job-101"); err != nil {
		t.Fatalf("attach job: %v", err)
	}
	if _, err := store.MarkDone(sc.ID, "job-101", time.Date(2026, 3, 18, 10, 0, 5, 0, now.Location())); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	if _, err := store.Get(sc.ID); err == nil {
		t.Fatalf("expected one-shot schedule to be removed")
	}
}

func TestRecurringScheduleAdvancesAndRepairClearsMissingJobs(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.Add(AddInput{
		Trigger: TriggerCron,
		Action:  ActionDispatch,
		Cron:    "0 1 * * *",
		Text:    "Run security scan",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("add schedule: %v", err)
	}
	next := sc.NextRun

	if _, err := store.AttachJob(sc.ID, "job-202"); err != nil {
		t.Fatalf("attach job: %v", err)
	}
	if err := store.Repair(func(string) bool { return false }); err != nil {
		t.Fatalf("repair: %v", err)
	}

	repaired, err := store.Get(sc.ID)
	if err != nil {
		t.Fatalf("get repaired schedule: %v", err)
	}
	if repaired.RunningJobID != "" {
		t.Fatalf("running job id = %q, want empty", repaired.RunningJobID)
	}

	if _, err := store.AttachJob(sc.ID, "job-203"); err != nil {
		t.Fatalf("reattach job: %v", err)
	}
	doneAt := time.Date(2026, 3, 18, 1, 5, 0, 0, now.Location())
	advanced, err := store.MarkDone(sc.ID, "job-203", doneAt)
	if err != nil {
		t.Fatalf("mark done: %v", err)
	}
	if !advanced.NextRun.After(doneAt) {
		t.Fatalf("next run = %v, want after %v", advanced.NextRun, doneAt)
	}
	if !advanced.NextRun.After(next) {
		t.Fatalf("next run = %v, want after original %v", advanced.NextRun, next)
	}
}

func TestParseAtSupportsRFC3339AndLocalFormats(t *testing.T) {
	loc := time.FixedZone("CDT", -5*60*60)

	got, err := parseAt("2026-03-18T10:00:00-05:00", loc)
	if err != nil {
		t.Fatalf("parseAt RFC3339: %v", err)
	}
	if got.Format(time.RFC3339) != "2026-03-18T10:00:00-05:00" {
		t.Fatalf("parseAt RFC3339 = %s", got.Format(time.RFC3339))
	}

	got, err = parseAt("2026-03-18 10:00", loc)
	if err != nil {
		t.Fatalf("parseAt local format: %v", err)
	}
	if got.Location() != loc {
		t.Fatalf("parseAt local location = %v, want %v", got.Location(), loc)
	}

	if _, err := parseAt("bad", loc); err == nil {
		t.Fatal("expected invalid parseAt error")
	}
}

func TestParseInCompoundDurationsAndValidation(t *testing.T) {
	got, err := parseIn("1d2h30m")
	if err != nil {
		t.Fatalf("parseIn: %v", err)
	}
	want := 26*time.Hour + 30*time.Minute
	if got != want {
		t.Fatalf("parseIn() = %v, want %v", got, want)
	}

	for _, raw := range []string{"", "0h", "2x", "10"} {
		if _, err := parseIn(raw); err == nil {
			t.Fatalf("expected parseIn(%q) to fail", raw)
		}
	}
}

func TestResolveAtValidation(t *testing.T) {
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	if _, err := resolveAt("2026-03-18T10:00:00-05:00", "1h", now, now.Location()); err == nil {
		t.Fatal("expected resolveAt to reject both at and in")
	}
	if _, err := resolveAt("", "", now, now.Location()); err == nil {
		t.Fatal("expected resolveAt to reject empty inputs")
	}

	got, err := resolveAt("", "2h", now, now.Location())
	if err != nil {
		t.Fatalf("resolveAt in: %v", err)
	}
	if !got.Equal(now.Add(2 * time.Hour)) {
		t.Fatalf("resolveAt in = %v, want %v", got, now.Add(2*time.Hour))
	}
}

func TestAddRejectsInvalidInputs(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	tests := []AddInput{
		{Trigger: TriggerAt, Action: ActionSend, At: "2026-03-18T10:00:00-05:00", Text: "   ", Now: now},
		{Trigger: TriggerAt, Action: "bad", At: "2026-03-18T10:00:00-05:00", Text: "hello", Now: now},
		{Trigger: "bad", Action: ActionSend, At: "2026-03-18T10:00:00-05:00", Text: "hello", Now: now},
		{Trigger: TriggerAt, Action: ActionSend, At: "2026-03-17T20:00:00-05:00", Text: "hello", Now: now},
		{Trigger: TriggerCron, Action: ActionSend, Cron: "", Text: "hello", Now: now},
	}

	for _, input := range tests {
		if _, err := store.Add(input); err == nil {
			t.Fatalf("expected Add(%+v) to fail", input)
		}
	}
}

func TestDueSortsAndSkipsRunningSchedules(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 3, 18, 10, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
	early := now.Add(-30 * time.Minute)
	late := now.Add(-5 * time.Minute)

	schedules := []Schedule{
		{ID: "future", Trigger: TriggerAt, NextRun: now.Add(1 * time.Hour), CreatedAt: now, Text: "future"},
		{ID: "late", Trigger: TriggerAt, NextRun: late, CreatedAt: now.Add(2 * time.Second), Text: "late"},
		{ID: "early", Trigger: TriggerAt, NextRun: early, CreatedAt: now.Add(1 * time.Second), Text: "early"},
		{ID: "running", Trigger: TriggerAt, NextRun: early, CreatedAt: now, Text: "running", RunningJobID: "job-99"},
	}
	if err := store.save(schedules); err != nil {
		t.Fatalf("save schedules: %v", err)
	}

	due, err := store.Due(now)
	if err != nil {
		t.Fatalf("Due(): %v", err)
	}
	if len(due) != 2 {
		t.Fatalf("Due() len = %d, want 2", len(due))
	}
	if due[0].ID != "early" || due[1].ID != "late" {
		t.Fatalf("Due() order = [%s %s], want [early late]", due[0].ID, due[1].ID)
	}
}

func TestDeleteAttachAndMarkDoneValidation(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.Add(AddInput{
		Trigger: TriggerCron,
		Action:  ActionDispatch,
		Cron:    "0 1 * * *",
		Text:    "Run scan",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}

	if err := store.Delete("missing"); !os.IsNotExist(err) {
		t.Fatalf("Delete(missing) err = %v, want os.ErrNotExist", err)
	}
	if _, err := store.AttachJob(sc.ID, ""); err == nil {
		t.Fatal("expected AttachJob zero to fail")
	}
	if _, err := store.AttachJob("missing", "job-10"); !os.IsNotExist(err) {
		t.Fatalf("AttachJob(missing) err = %v, want os.ErrNotExist", err)
	}
	if _, err := store.AttachJob(sc.ID, "job-10"); err != nil {
		t.Fatalf("AttachJob(): %v", err)
	}
	if err := store.Delete(sc.ID); err == nil || !strings.Contains(err.Error(), "is running via job job-10") {
		t.Fatalf("Delete(running) err = %v", err)
	}
	if _, err := store.AttachJob(sc.ID, "job-11"); err == nil {
		t.Fatal("expected duplicate AttachJob to fail")
	}
	if _, err := store.MarkDone(sc.ID, "job-12", now.Add(time.Hour)); err == nil {
		t.Fatal("expected MarkDone wrong job to fail")
	}
}

func TestNextCronRunValidation(t *testing.T) {
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	if _, err := nextCronRun("", now, now.Location()); err == nil {
		t.Fatal("expected empty cron error")
	}
	got, err := nextCronRun("0 1 * * *", now, now.Location())
	if err != nil {
		t.Fatalf("nextCronRun(): %v", err)
	}
	if !got.After(now) {
		t.Fatalf("nextCronRun() = %v, want after %v", got, now)
	}
}

func TestLoadUpgradesLegacyCronSchedule(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
	legacy := fileData{
		Schedules: []Schedule{
			{
				ID:             "sch-legacy-cron",
				Trigger:        TriggerCron,
				Action:         ActionDispatch,
				Cron:           "0 1 * * *",
				Text:           "Run scan",
				ConversationID: "telegram:chat",
				Backend:        "claude",
				CreatedAt:      now,
			},
		},
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy schedules: %v", err)
	}
	if err := os.WriteFile(store.path, data, 0600); err != nil {
		t.Fatalf("write legacy schedules: %v", err)
	}

	schedules, err := store.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("List() len = %d, want 1", len(schedules))
	}

	sc := schedules[0]
	if sc.Trigger != TriggerCalendar {
		t.Fatalf("trigger = %s, want %s", sc.Trigger, TriggerCalendar)
	}
	if sc.Cron != "" {
		t.Fatalf("legacy cron field = %q, want empty", sc.Cron)
	}
	if sc.Calendar == nil {
		t.Fatal("calendar = nil, want parsed calendar")
	}
	if sc.Calendar.Cron != "0 1 * * *" {
		t.Fatalf("calendar cron = %q, want %q", sc.Calendar.Cron, "0 1 * * *")
	}
	if sc.Calendar.CronSpec() != "0 1 * * *" {
		t.Fatalf("calendar spec = %q, want %q", sc.Calendar.CronSpec(), "0 1 * * *")
	}
	if sc.ManagedBy != ManagedByInProcess {
		t.Fatalf("managed_by = %q, want %q", sc.ManagedBy, ManagedByInProcess)
	}
	if sc.SyncState != SyncStateFallback {
		t.Fatalf("sync_state = %q, want %q", sc.SyncState, SyncStateFallback)
	}
	wantNextRun := time.Date(2026, 3, 18, 1, 0, 0, 0, now.Location())
	if !sc.NextRun.Equal(wantNextRun) {
		t.Fatalf("next run = %v, want %v", sc.NextRun, wantNextRun)
	}
}

func TestLoadBackfillsLegacyIntervalSchedule(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
	legacy := fileData{
		Schedules: []Schedule{
			{
				ID:        "sch-legacy-interval",
				Trigger:   TriggerInterval,
				Action:    ActionDispatch,
				Interval:  "90m",
				Text:      "Run cleanup",
				CreatedAt: now,
			},
		},
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy schedules: %v", err)
	}
	if err := os.WriteFile(store.path, data, 0600); err != nil {
		t.Fatalf("write legacy schedules: %v", err)
	}

	schedules, err := store.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("List() len = %d, want 1", len(schedules))
	}

	sc := schedules[0]
	if sc.Trigger != TriggerInterval {
		t.Fatalf("trigger = %s, want %s", sc.Trigger, TriggerInterval)
	}
	if sc.Interval != "1h30m0s" {
		t.Fatalf("interval = %q, want %q", sc.Interval, "1h30m0s")
	}
	if !sc.NextRun.Equal(now.Add(90 * time.Minute)) {
		t.Fatalf("next run = %v, want %v", sc.NextRun, now.Add(90*time.Minute))
	}
	if sc.ManagedBy != ManagedByInProcess {
		t.Fatalf("managed_by = %q, want %q", sc.ManagedBy, ManagedByInProcess)
	}
	if sc.SyncState != SyncStateFallback {
		t.Fatalf("sync_state = %q, want %q", sc.SyncState, SyncStateFallback)
	}
}

func TestAddPersistsCanonicalCalendarRepresentation(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.Add(AddInput{
		Trigger: TriggerCron,
		Action:  ActionDispatch,
		Cron:    "0 1 * * *",
		Text:    "Run scan",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("add schedule: %v", err)
	}
	if sc.Trigger != TriggerCalendar {
		t.Fatalf("trigger = %s, want %s", sc.Trigger, TriggerCalendar)
	}

	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatalf("read schedules file: %v", err)
	}
	var doc fileData
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal schedules file: %v", err)
	}
	if len(doc.Schedules) != 1 {
		t.Fatalf("stored schedules len = %d, want 1", len(doc.Schedules))
	}

	stored := doc.Schedules[0]
	if stored.Trigger != TriggerCalendar {
		t.Fatalf("stored trigger = %s, want %s", stored.Trigger, TriggerCalendar)
	}
	if stored.Cron != "" {
		t.Fatalf("stored legacy cron field = %q, want empty", stored.Cron)
	}
	if stored.Calendar == nil {
		t.Fatal("stored calendar = nil, want canonical calendar")
	}
	if stored.Calendar.Cron != "0 1 * * *" {
		t.Fatalf("stored calendar cron = %q, want %q", stored.Calendar.Cron, "0 1 * * *")
	}
	if stored.ManagedBy != ManagedByInProcess {
		t.Fatalf("stored managed_by = %q, want %q", stored.ManagedBy, ManagedByInProcess)
	}
	if stored.SyncState != SyncStateFallback {
		t.Fatalf("stored sync_state = %q, want %q", stored.SyncState, SyncStateFallback)
	}
}

func TestSaveCanonicalizesLegacyScheduleRepresentation(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	if err := store.save([]Schedule{
		{
			ID:             "sch-legacy-save",
			Trigger:        TriggerCron,
			Action:         ActionDispatch,
			Cron:           "0 1 * * *",
			Text:           "Run scan",
			ConversationID: "telegram:chat",
			Backend:        "claude",
			CreatedAt:      now,
		},
	}); err != nil {
		t.Fatalf("save legacy schedule: %v", err)
	}

	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatalf("read schedules file: %v", err)
	}
	var doc fileData
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal schedules file: %v", err)
	}
	if len(doc.Schedules) != 1 {
		t.Fatalf("stored schedules len = %d, want 1", len(doc.Schedules))
	}

	stored := doc.Schedules[0]
	if stored.Trigger != TriggerCalendar {
		t.Fatalf("stored trigger = %s, want %s", stored.Trigger, TriggerCalendar)
	}
	if stored.Cron != "" {
		t.Fatalf("stored legacy cron field = %q, want empty", stored.Cron)
	}
	if stored.Calendar == nil {
		t.Fatal("stored calendar = nil, want canonical calendar")
	}
	if stored.Calendar.Cron != "0 1 * * *" {
		t.Fatalf("stored calendar cron = %q, want %q", stored.Calendar.Cron, "0 1 * * *")
	}
	if stored.ManagedBy != ManagedByInProcess {
		t.Fatalf("stored managed_by = %q, want %q", stored.ManagedBy, ManagedByInProcess)
	}
	if stored.SyncState != SyncStateFallback {
		t.Fatalf("stored sync_state = %q, want %q", stored.SyncState, SyncStateFallback)
	}
	wantNextRun := time.Date(2026, 3, 18, 1, 0, 0, 0, now.Location())
	if !stored.NextRun.Equal(wantNextRun) {
		t.Fatalf("stored next run = %v, want %v", stored.NextRun, wantNextRun)
	}
}
