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

func writeScheduleFixture(t *testing.T, path string, doc any) {
	t.Helper()
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal schedule fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write schedule fixture: %v", err)
	}
}

func readStoredScheduleRecord(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schedules file: %v", err)
	}
	var doc struct {
		Schedules []map[string]json.RawMessage `json:"schedules"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal raw schedules file: %v", err)
	}
	if len(doc.Schedules) != 1 {
		t.Fatalf("stored schedules len = %d, want 1", len(doc.Schedules))
	}
	return doc.Schedules[0]
}

func decodeRawObject(t *testing.T, raw json.RawMessage, label string) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal %s: %v", label, err)
	}
	return obj
}

func expectMissingKey(t *testing.T, obj map[string]json.RawMessage, key string) {
	t.Helper()
	if _, ok := obj[key]; ok {
		t.Fatalf("unexpected key %q in stored object", key)
	}
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

	if sc.Spec.Trigger != TriggerAt {
		t.Fatalf("trigger = %s, want %s", sc.Spec.Trigger, TriggerAt)
	}
	if !sc.NextRun.Equal(sc.Spec.At) {
		t.Fatalf("next run = %v, want %v", sc.NextRun, sc.Spec.At)
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
	if !sc.Spec.At.Equal(want) {
		t.Fatalf("at = %v, want %v", sc.Spec.At, want)
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

	if sc.Spec.Trigger != TriggerInterval {
		t.Fatalf("trigger = %s, want %s", sc.Spec.Trigger, TriggerInterval)
	}
	if sc.Spec.Interval != "1h30m0s" {
		t.Fatalf("interval = %q, want %q", sc.Spec.Interval, "1h30m0s")
	}
	if !sc.NextRun.Equal(now.Add(90 * time.Minute)) {
		t.Fatalf("next run = %v, want %v", sc.NextRun, now.Add(90*time.Minute))
	}
	if sc.Sync.ManagedBy != ManagedByInProcess {
		t.Fatalf("managed_by = %q, want %q", sc.Sync.ManagedBy, ManagedByInProcess)
	}
	if sc.Sync.State != SyncStateFallback {
		t.Fatalf("sync_state = %q, want %q", sc.Sync.State, SyncStateFallback)
	}
}

func TestMarkDoneAdvancesIntervalSchedule(t *testing.T) {
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
	if _, err := store.AttachJob(sc.ID, "job-interval"); err != nil {
		t.Fatalf("attach job: %v", err)
	}

	doneAt := sc.NextRun.Add(5 * time.Second)
	advanced, err := store.MarkDone(sc.ID, "job-interval", doneAt)
	if err != nil {
		t.Fatalf("mark done: %v", err)
	}
	if !advanced.LastRun.Equal(doneAt) {
		t.Fatalf("last run = %v, want %v", advanced.LastRun, doneAt)
	}
	if !advanced.NextRun.Equal(doneAt.Add(90 * time.Minute)) {
		t.Fatalf("next run = %v, want %v", advanced.NextRun, doneAt.Add(90*time.Minute))
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

func TestParseEveryValidation(t *testing.T) {
	got, err := parseEvery("90m")
	if err != nil {
		t.Fatalf("parseEvery(): %v", err)
	}
	if got != 90*time.Minute {
		t.Fatalf("parseEvery() = %v, want %v", got, 90*time.Minute)
	}

	for _, raw := range []string{"", "30s", "0h", "2x"} {
		if _, err := parseEvery(raw); err == nil {
			t.Fatalf("expected parseEvery(%q) to fail", raw)
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
		{Trigger: TriggerInterval, Action: ActionDispatch, Every: "30s", Text: "hello", Now: now},
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
		{ID: "future", Spec: ScheduleSpec{Trigger: TriggerAt}, NextRun: now.Add(1 * time.Hour), CreatedAt: now, Text: "future"},
		{ID: "late", Spec: ScheduleSpec{Trigger: TriggerAt}, NextRun: late, CreatedAt: now.Add(2 * time.Second), Text: "late"},
		{ID: "early", Spec: ScheduleSpec{Trigger: TriggerAt}, NextRun: early, CreatedAt: now.Add(1 * time.Second), Text: "early"},
		{ID: "running", Spec: ScheduleSpec{Trigger: TriggerAt}, NextRun: early, CreatedAt: now, Text: "running", RunningJobID: "job-99"},
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
	writeScheduleFixture(t, store.path, map[string]any{
		"schedules": []map[string]any{
			{
				"id":              "sch-legacy-cron",
				"trigger":         TriggerCron,
				"action":          ActionDispatch,
				"cron":            "0 1 * * *",
				"text":            "Run scan",
				"conversation_id": "telegram:chat",
				"backend":         "claude",
				"created_at":      now,
			},
		},
	})

	schedules, err := store.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("List() len = %d, want 1", len(schedules))
	}

	sc := schedules[0]
	if sc.Spec.Trigger != TriggerCalendar {
		t.Fatalf("trigger = %s, want %s", sc.Spec.Trigger, TriggerCalendar)
	}
	if sc.Spec.legacyCronSpec() != "" {
		t.Fatalf("legacy cron field = %q, want empty", sc.Spec.legacyCronSpec())
	}
	if sc.Spec.Calendar == nil {
		t.Fatal("calendar = nil, want parsed calendar")
	}
	if sc.Spec.Calendar.Cron != "0 1 * * *" {
		t.Fatalf("calendar cron = %q, want %q", sc.Spec.Calendar.Cron, "0 1 * * *")
	}
	if sc.Spec.Calendar.CronSpec() != "0 1 * * *" {
		t.Fatalf("calendar spec = %q, want %q", sc.Spec.Calendar.CronSpec(), "0 1 * * *")
	}
	if sc.Sync.ManagedBy != ManagedByInProcess {
		t.Fatalf("managed_by = %q, want %q", sc.Sync.ManagedBy, ManagedByInProcess)
	}
	if sc.Sync.State != SyncStateFallback {
		t.Fatalf("sync_state = %q, want %q", sc.Sync.State, SyncStateFallback)
	}
	wantNextRun := time.Date(2026, 3, 18, 1, 0, 0, 0, now.Location())
	if !sc.NextRun.Equal(wantNextRun) {
		t.Fatalf("next run = %v, want %v", sc.NextRun, wantNextRun)
	}
}

func TestLoadBackfillsLegacyIntervalSchedule(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
	writeScheduleFixture(t, store.path, map[string]any{
		"schedules": []map[string]any{
			{
				"id":         "sch-legacy-interval",
				"trigger":    TriggerInterval,
				"action":     ActionDispatch,
				"interval":   "90m",
				"text":       "Run cleanup",
				"created_at": now,
			},
		},
	})

	schedules, err := store.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("List() len = %d, want 1", len(schedules))
	}

	sc := schedules[0]
	if sc.Spec.Trigger != TriggerInterval {
		t.Fatalf("trigger = %s, want %s", sc.Spec.Trigger, TriggerInterval)
	}
	if sc.Spec.Interval != "1h30m0s" {
		t.Fatalf("interval = %q, want %q", sc.Spec.Interval, "1h30m0s")
	}
	if !sc.NextRun.Equal(now.Add(90 * time.Minute)) {
		t.Fatalf("next run = %v, want %v", sc.NextRun, now.Add(90*time.Minute))
	}
	if sc.Sync.ManagedBy != ManagedByInProcess {
		t.Fatalf("managed_by = %q, want %q", sc.Sync.ManagedBy, ManagedByInProcess)
	}
	if sc.Sync.State != SyncStateFallback {
		t.Fatalf("sync_state = %q, want %q", sc.Sync.State, SyncStateFallback)
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
	if sc.Spec.Trigger != TriggerCalendar {
		t.Fatalf("trigger = %s, want %s", sc.Spec.Trigger, TriggerCalendar)
	}

	stored := readStoredScheduleRecord(t, store.path)
	spec := decodeRawObject(t, stored["spec"], "stored spec")
	if got := strings.Trim(string(spec["trigger"]), `"`); got != string(TriggerCalendar) {
		t.Fatalf("stored trigger = %s, want %s", got, TriggerCalendar)
	}
	calendar := decodeRawObject(t, spec["calendar"], "stored calendar")
	if got := strings.Trim(string(calendar["cron"]), `"`); got != "0 1 * * *" {
		t.Fatalf("stored calendar cron = %q, want %q", got, "0 1 * * *")
	}
	sync := decodeRawObject(t, stored["sync"], "stored sync")
	if got := strings.Trim(string(sync["managed_by"]), `"`); got != ManagedByInProcess {
		t.Fatalf("stored managed_by = %q, want %q", got, ManagedByInProcess)
	}
	if got := strings.Trim(string(sync["state"]), `"`); got != SyncStateFallback {
		t.Fatalf("stored sync state = %q, want %q", got, SyncStateFallback)
	}
	for _, key := range []string{"trigger", "at", "interval", "calendar", "cron", "managed_by", "sync_state", "sync_error"} {
		expectMissingKey(t, stored, key)
	}
}

func TestSaveCanonicalizesLegacyScheduleRepresentation(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	if err := store.save([]Schedule{
		{
			ID:     "sch-legacy-save",
			Action: ActionDispatch,
			Spec: ScheduleSpec{
				Trigger:    TriggerCron,
				legacyCron: "0 1 * * *",
			},
			Text:           "Run scan",
			ConversationID: "telegram:chat",
			Backend:        "claude",
			CreatedAt:      now,
		},
	}); err != nil {
		t.Fatalf("save legacy schedule: %v", err)
	}

	stored := readStoredScheduleRecord(t, store.path)
	spec := decodeRawObject(t, stored["spec"], "stored spec")
	if got := strings.Trim(string(spec["trigger"]), `"`); got != string(TriggerCalendar) {
		t.Fatalf("stored trigger = %s, want %s", got, TriggerCalendar)
	}
	calendar := decodeRawObject(t, spec["calendar"], "stored calendar")
	if got := strings.Trim(string(calendar["cron"]), `"`); got != "0 1 * * *" {
		t.Fatalf("stored calendar cron = %q, want %q", got, "0 1 * * *")
	}
	sync := decodeRawObject(t, stored["sync"], "stored sync")
	if got := strings.Trim(string(sync["managed_by"]), `"`); got != ManagedByInProcess {
		t.Fatalf("stored managed_by = %q, want %q", got, ManagedByInProcess)
	}
	if got := strings.Trim(string(sync["state"]), `"`); got != SyncStateFallback {
		t.Fatalf("stored sync state = %q, want %q", got, SyncStateFallback)
	}
	for _, key := range []string{"trigger", "at", "interval", "calendar", "cron", "managed_by", "sync_state", "sync_error"} {
		expectMissingKey(t, stored, key)
	}

	schedules, err := store.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("List() len = %d, want 1", len(schedules))
	}
	wantNextRun := time.Date(2026, 3, 18, 1, 0, 0, 0, now.Location())
	if !schedules[0].NextRun.Equal(wantNextRun) {
		t.Fatalf("stored next run = %v, want %v", schedules[0].NextRun, wantNextRun)
	}
}
