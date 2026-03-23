package scheduler

import (
	"path/filepath"
	"testing"
	"time"
)

type recordingInProcessBackend struct {
	installs []string
	updates  []string
	removes  []string
}

func (b *recordingInProcessBackend) Name() string {
	return ManagedByInProcess
}

func (b *recordingInProcessBackend) Install(sc Schedule) error {
	b.installs = append(b.installs, sc.ID)
	return nil
}

func (b *recordingInProcessBackend) Update(sc Schedule) error {
	b.updates = append(b.updates, sc.ID)
	return nil
}

func (b *recordingInProcessBackend) Remove(id string) error {
	b.removes = append(b.removes, id)
	return nil
}

func (b *recordingInProcessBackend) Supports() BackendCaps {
	return BackendCaps{}
}

func testStoreWithBackends(t *testing.T, backends ...ScheduleBackend) *Store {
	t.Helper()
	loc := time.FixedZone("CDT", -5*60*60)
	return newStoreWithBackends(filepath.Join(t.TempDir(), "schedules.json"), loc, newBackendReconciler(backends...))
}

func TestAddMaterializesThroughInProcessBackend(t *testing.T) {
	backend := &recordingInProcessBackend{}
	store := testStoreWithBackends(t, backend)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.Add(AddInput{
		Trigger: TriggerInterval,
		Action:  ActionDispatch,
		Every:   "90m",
		Text:    "Run cleanup",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}
	if len(backend.installs) != 1 || backend.installs[0] != sc.ID {
		t.Fatalf("installs = %v, want [%s]", backend.installs, sc.ID)
	}
	if len(backend.updates) != 0 {
		t.Fatalf("updates = %v, want none", backend.updates)
	}
	if sc.Sync.ManagedBy != ManagedByInProcess {
		t.Fatalf("managed_by = %q, want %q", sc.Sync.ManagedBy, ManagedByInProcess)
	}
	if sc.Sync.State != SyncStateFallback {
		t.Fatalf("sync_state = %q, want %q", sc.Sync.State, SyncStateFallback)
	}
}

func TestLoadBackfillsSyncViaReconcilerWithoutMaterializing(t *testing.T) {
	backend := &recordingInProcessBackend{}
	store := testStoreWithBackends(t, backend)
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
	if len(backend.installs) != 0 || len(backend.updates) != 0 || len(backend.removes) != 0 {
		t.Fatalf("backend calls = installs %v updates %v removes %v, want none", backend.installs, backend.updates, backend.removes)
	}
	if schedules[0].Sync.ManagedBy != ManagedByInProcess {
		t.Fatalf("managed_by = %q, want %q", schedules[0].Sync.ManagedBy, ManagedByInProcess)
	}
	if schedules[0].Sync.State != SyncStateFallback {
		t.Fatalf("sync_state = %q, want %q", schedules[0].Sync.State, SyncStateFallback)
	}
}

func TestDeleteRemovesThroughManagedBackend(t *testing.T) {
	backend := &recordingInProcessBackend{}
	store := testStoreWithBackends(t, backend)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.Add(AddInput{
		Trigger: TriggerInterval,
		Action:  ActionDispatch,
		Every:   "90m",
		Text:    "Run cleanup",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}
	backend.removes = nil

	if err := store.Delete(sc.ID); err != nil {
		t.Fatalf("Delete(): %v", err)
	}
	if len(backend.removes) != 1 || backend.removes[0] != sc.ID {
		t.Fatalf("removes = %v, want [%s]", backend.removes, sc.ID)
	}
}

func TestMarkDoneOneShotRemovesThroughManagedBackend(t *testing.T) {
	backend := &recordingInProcessBackend{}
	store := testStoreWithBackends(t, backend)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.Add(AddInput{
		Trigger: TriggerAt,
		Action:  ActionSend,
		At:      "2026-03-18T10:00:00-05:00",
		Text:    "Call John",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}
	if _, err := store.AttachJob(sc.ID, "job-101"); err != nil {
		t.Fatalf("AttachJob(): %v", err)
	}
	backend.removes = nil

	if _, err := store.MarkDone(sc.ID, "job-101", time.Date(2026, 3, 18, 10, 0, 5, 0, now.Location())); err != nil {
		t.Fatalf("MarkDone(): %v", err)
	}
	if len(backend.removes) != 1 || backend.removes[0] != sc.ID {
		t.Fatalf("removes = %v, want [%s]", backend.removes, sc.ID)
	}
}
