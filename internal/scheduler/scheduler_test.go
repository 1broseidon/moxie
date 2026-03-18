package scheduler

import (
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
	if _, err := store.AttachJob(sc.ID, -101); err != nil {
		t.Fatalf("attach job: %v", err)
	}
	if _, err := store.MarkDone(sc.ID, -101, time.Date(2026, 3, 18, 10, 0, 5, 0, now.Location())); err != nil {
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

	if _, err := store.AttachJob(sc.ID, -202); err != nil {
		t.Fatalf("attach job: %v", err)
	}
	if err := store.Repair(func(int) bool { return false }); err != nil {
		t.Fatalf("repair: %v", err)
	}

	repaired, err := store.Get(sc.ID)
	if err != nil {
		t.Fatalf("get repaired schedule: %v", err)
	}
	if repaired.RunningJobID != 0 {
		t.Fatalf("running job id = %d, want 0", repaired.RunningJobID)
	}

	if _, err := store.AttachJob(sc.ID, -203); err != nil {
		t.Fatalf("reattach job: %v", err)
	}
	doneAt := time.Date(2026, 3, 18, 1, 5, 0, 0, now.Location())
	advanced, err := store.MarkDone(sc.ID, -203, doneAt)
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
		{ID: "future", NextRun: now.Add(1 * time.Hour), CreatedAt: now, Text: "future"},
		{ID: "late", NextRun: late, CreatedAt: now.Add(2 * time.Second), Text: "late"},
		{ID: "early", NextRun: early, CreatedAt: now.Add(1 * time.Second), Text: "early"},
		{ID: "running", NextRun: early, CreatedAt: now, Text: "running", RunningJobID: 99},
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
	if _, err := store.AttachJob(sc.ID, 0); err == nil {
		t.Fatal("expected AttachJob zero to fail")
	}
	if _, err := store.AttachJob("missing", 10); !os.IsNotExist(err) {
		t.Fatalf("AttachJob(missing) err = %v, want os.ErrNotExist", err)
	}
	if _, err := store.AttachJob(sc.ID, 10); err != nil {
		t.Fatalf("AttachJob(): %v", err)
	}
	if err := store.Delete(sc.ID); err == nil || !strings.Contains(err.Error(), "is running via job 10") {
		t.Fatalf("Delete(running) err = %v", err)
	}
	if _, err := store.AttachJob(sc.ID, 11); err == nil {
		t.Fatal("expected duplicate AttachJob to fail")
	}
	if _, err := store.MarkDone(sc.ID, 12, now.Add(time.Hour)); err == nil {
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
