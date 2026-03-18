package scheduler

import (
	"path/filepath"
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
