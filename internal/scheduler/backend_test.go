package scheduler

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

type recordingCommandRunner struct {
	failures       map[string]error
	prefixFailures map[string]error
	commands       []string
}

func (r *recordingCommandRunner) run(name string, args ...string) ([]byte, error) {
	command := strings.TrimSpace(name + " " + strings.Join(args, " "))
	r.commands = append(r.commands, command)
	if err := r.failures[command]; err != nil {
		return []byte(err.Error()), err
	}
	for prefix, err := range r.prefixFailures {
		if strings.HasPrefix(command, prefix) {
			return []byte(err.Error()), err
		}
	}
	return nil, nil
}

type recordingNativeBackend struct {
	name       string
	caps       BackendCaps
	supportErr error
	installErr error
	updateErr  error
	removeErr  error
	installs   []string
	updates    []string
	removes    []string
}

func (b *recordingNativeBackend) Name() string {
	return b.name
}

func (b *recordingNativeBackend) Install(sc Schedule) error {
	b.installs = append(b.installs, sc.ID)
	return b.installErr
}

func (b *recordingNativeBackend) Update(sc Schedule) error {
	b.updates = append(b.updates, sc.ID)
	return b.updateErr
}

func (b *recordingNativeBackend) Remove(id string) error {
	b.removes = append(b.removes, id)
	return b.removeErr
}

func (b *recordingNativeBackend) Supports() BackendCaps {
	return b.caps
}

func (b *recordingNativeBackend) SupportsSchedule(Schedule) bool {
	return b.supportErr == nil
}

func (b *recordingNativeBackend) SupportError(Schedule) error {
	return b.supportErr
}

func testLaunchdBackend(t *testing.T) (*launchdBackend, *recordingCommandRunner, string) {
	t.Helper()
	home := t.TempDir()
	runner := &recordingCommandRunner{failures: map[string]error{}, prefixFailures: map[string]error{}}
	backend := &launchdBackend{
		homeDir: func() (string, error) { return home, nil },
		binaryPath: func() (string, error) {
			return "/usr/local/bin/moxie", nil
		},
		now: func() time.Time {
			return time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
		},
		uid: func() int { return 501 },
		env: func() map[string]string {
			return map[string]string{
				"PATH": "/opt/homebrew/bin:/usr/bin:/bin",
				"HOME": home,
			}
		},
		runCommand: runner.run,
	}
	return backend, runner, home
}

func testSchTasksBackend(t *testing.T) (*schtasksBackend, *recordingCommandRunner) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", filepath.Join(base, "LocalAppData"))
	runner := &recordingCommandRunner{failures: map[string]error{}, prefixFailures: map[string]error{}}
	backend := &schtasksBackend{
		binaryPath: func() (string, error) {
			return `C:\Program Files\Moxie\moxie.exe`, nil
		},
		currentUser: func() (string, error) {
			return `DOMAIN\tester`, nil
		},
		now: func() time.Time {
			return time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
		},
		runCommand: runner.run,
	}
	return backend, runner
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

func TestAddMaterializesThroughLaunchdWhenSupported(t *testing.T) {
	launchdBackend, runner, home := testLaunchdBackend(t)
	fallback := &recordingInProcessBackend{}
	store := testStoreWithBackends(t, launchdBackend, fallback)
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
	if sc.Sync.ManagedBy != ManagedByLaunchd {
		t.Fatalf("managed_by = %q, want %q", sc.Sync.ManagedBy, ManagedByLaunchd)
	}
	if sc.Sync.State != SyncStateSynced {
		t.Fatalf("sync_state = %q, want %q", sc.Sync.State, SyncStateSynced)
	}
	if sc.Sync.Error != "" {
		t.Fatalf("sync_error = %q, want empty", sc.Sync.Error)
	}
	if len(fallback.installs) != 0 {
		t.Fatalf("fallback installs = %v, want none", fallback.installs)
	}
	plistPath := launchdSchedulePlistPath(home, sc.ID)
	wantBootstrap := "launchctl bootstrap gui/501 " + plistPath
	if len(runner.commands) != 1 || runner.commands[0] != wantBootstrap {
		t.Fatalf("commands = %v, want [%q]", runner.commands, wantBootstrap)
	}
	content, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	for _, needle := range []string{
		"<string>/usr/local/bin/moxie</string>",
		"<string>schedule</string>",
		"<string>fire</string>",
		"<string>" + sc.ID + "</string>",
		"<key>StartInterval</key>",
		"<integer>5400</integer>",
	} {
		if !strings.Contains(string(content), needle) {
			t.Fatalf("plist missing %q: %s", needle, string(content))
		}
	}
	stored := readStoredScheduleRecord(t, store.path)
	sync := decodeRawObject(t, stored["sync"], "stored sync")
	if got := strings.Trim(string(sync["managed_by"]), `"`); got != ManagedByLaunchd {
		t.Fatalf("stored managed_by = %q, want %q", got, ManagedByLaunchd)
	}
	if got := strings.Trim(string(sync["state"]), `"`); got != SyncStateSynced {
		t.Fatalf("stored sync state = %q, want %q", got, SyncStateSynced)
	}
	due, err := store.Due(now.Add(3 * time.Hour))
	if err != nil {
		t.Fatalf("Due(): %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("Due() = %v, want no in-process launchd schedules", due)
	}
}

func TestAddRelativeOneShotRoundsUpAndSyncsToLaunchd(t *testing.T) {
	launchdBackend, runner, home := testLaunchdBackend(t)
	fallback := &recordingInProcessBackend{}
	store := testStoreWithBackends(t, launchdBackend, fallback)
	now := time.Date(2026, 3, 17, 21, 0, 30, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.Add(AddInput{
		Trigger: TriggerAt,
		Action:  ActionSend,
		In:      "2m",
		Text:    "Call John",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}
	wantAt := time.Date(2026, 3, 17, 21, 3, 0, 0, now.Location())
	if !sc.Spec.At.Equal(wantAt) {
		t.Fatalf("at = %v, want %v", sc.Spec.At, wantAt)
	}
	if sc.Sync.ManagedBy != ManagedByLaunchd {
		t.Fatalf("managed_by = %q, want %q", sc.Sync.ManagedBy, ManagedByLaunchd)
	}
	if len(fallback.installs) != 0 {
		t.Fatalf("fallback installs = %v, want none", fallback.installs)
	}
	plistPath := launchdSchedulePlistPath(home, sc.ID)
	wantBootstrap := "launchctl bootstrap gui/501 " + plistPath
	if len(runner.commands) != 1 || runner.commands[0] != wantBootstrap {
		t.Fatalf("commands = %v, want [%q]", runner.commands, wantBootstrap)
	}
	content, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	// The plist uses time.Local, so expected values depend on the CI timezone.
	localAt := wantAt.In(time.Local)
	for _, needle := range []string{
		"<key>Minute</key>",
		fmt.Sprintf("<integer>%d</integer>", localAt.Minute()),
		"<key>Hour</key>",
		fmt.Sprintf("<integer>%d</integer>", localAt.Hour()),
	} {
		if !strings.Contains(string(content), needle) {
			t.Fatalf("plist missing %q: %s", needle, string(content))
		}
	}
}

func TestAddFallsBackWhenLaunchdCannotRepresentSchedule(t *testing.T) {
	launchdBackend, runner, _ := testLaunchdBackend(t)
	fallback := &recordingInProcessBackend{}
	store := testStoreWithBackends(t, launchdBackend, fallback)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.Add(AddInput{
		Trigger: TriggerAt,
		Action:  ActionSend,
		At:      "2026-03-18T10:00:05-05:00",
		Text:    "Call John",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}
	if sc.Sync.ManagedBy != ManagedByInProcess {
		t.Fatalf("managed_by = %q, want %q", sc.Sync.ManagedBy, ManagedByInProcess)
	}
	if sc.Sync.State != SyncStateFallback {
		t.Fatalf("sync_state = %q, want %q", sc.Sync.State, SyncStateFallback)
	}
	if !strings.Contains(sc.Sync.Error, "minute precision") {
		t.Fatalf("sync_error = %q, want minute precision reason", sc.Sync.Error)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("launchd commands = %v, want none", runner.commands)
	}
	if len(fallback.installs) != 1 || fallback.installs[0] != sc.ID {
		t.Fatalf("fallback installs = %v, want [%s]", fallback.installs, sc.ID)
	}
	stored := readStoredScheduleRecord(t, store.path)
	sync := decodeRawObject(t, stored["sync"], "stored sync")
	if got := strings.Trim(string(sync["managed_by"]), `"`); got != ManagedByInProcess {
		t.Fatalf("stored managed_by = %q, want %q", got, ManagedByInProcess)
	}
	if got := strings.Trim(string(sync["state"]), `"`); got != SyncStateFallback {
		t.Fatalf("stored sync state = %q, want %q", got, SyncStateFallback)
	}
	if got := strings.Trim(string(sync["error"]), `"`); !strings.Contains(got, "minute precision") {
		t.Fatalf("stored sync error = %q, want minute precision reason", got)
	}
}

func TestAddFallsBackWhenLaunchdAtScheduleNeedsYearPrecision(t *testing.T) {
	launchdBackend, runner, _ := testLaunchdBackend(t)
	fallback := &recordingInProcessBackend{}
	store := testStoreWithBackends(t, launchdBackend, fallback)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.Add(AddInput{
		Trigger: TriggerAt,
		Action:  ActionSend,
		At:      "2028-04-01T10:00:00-05:00",
		Text:    "Review annual plan",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}
	if sc.Sync.ManagedBy != ManagedByInProcess {
		t.Fatalf("managed_by = %q, want %q", sc.Sync.ManagedBy, ManagedByInProcess)
	}
	if sc.Sync.State != SyncStateFallback {
		t.Fatalf("sync_state = %q, want %q", sc.Sync.State, SyncStateFallback)
	}
	if !strings.Contains(sc.Sync.Error, "year precision") {
		t.Fatalf("sync_error = %q, want year precision reason", sc.Sync.Error)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("launchd commands = %v, want none", runner.commands)
	}
	if len(fallback.installs) != 1 || fallback.installs[0] != sc.ID {
		t.Fatalf("fallback installs = %v, want [%s]", fallback.installs, sc.ID)
	}
}

func TestAddFallsBackWhenLaunchdInstallFails(t *testing.T) {
	launchdBackend, runner, home := testLaunchdBackend(t)
	fallback := &recordingInProcessBackend{}
	store := testStoreWithBackends(t, launchdBackend, fallback)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.buildSchedule(AddInput{
		Trigger: TriggerInterval,
		Action:  ActionDispatch,
		Every:   "90m",
		Text:    "Run cleanup",
		Now:     now,
	}, now)
	if err != nil {
		t.Fatalf("buildSchedule(): %v", err)
	}
	bootstrap := "launchctl bootstrap gui/501 " + launchdSchedulePlistPath(home, sc.ID)
	runner.failures[bootstrap] = errors.New("bootstrap failed")

	sc, err = store.Add(AddInput{
		Trigger: TriggerInterval,
		Action:  ActionDispatch,
		Every:   "90m",
		Text:    "Run cleanup",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}
	if sc.Sync.ManagedBy != ManagedByInProcess {
		t.Fatalf("managed_by = %q, want %q", sc.Sync.ManagedBy, ManagedByInProcess)
	}
	if sc.Sync.State != SyncStateFallback {
		t.Fatalf("sync_state = %q, want %q", sc.Sync.State, SyncStateFallback)
	}
	if !strings.Contains(sc.Sync.Error, "bootstrap failed") {
		t.Fatalf("sync_error = %q, want bootstrap failure", sc.Sync.Error)
	}
	if len(fallback.installs) != 1 || fallback.installs[0] != sc.ID {
		t.Fatalf("fallback installs = %v, want [%s]", fallback.installs, sc.ID)
	}
	if _, statErr := os.Stat(launchdSchedulePlistPath(home, sc.ID)); !os.IsNotExist(statErr) {
		t.Fatalf("plist stat err = %v, want os.ErrNotExist", statErr)
	}
}

func TestAddMaterializesThroughSchTasksWhenSupported(t *testing.T) {
	schtasksBackend, runner := testSchTasksBackend(t)
	fallback := &recordingInProcessBackend{}
	store := testStoreWithBackends(t, schtasksBackend, fallback)
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
	if sc.Sync.ManagedBy != ManagedBySchTasks {
		t.Fatalf("managed_by = %q, want %q", sc.Sync.ManagedBy, ManagedBySchTasks)
	}
	if sc.Sync.State != SyncStateSynced {
		t.Fatalf("sync_state = %q, want %q", sc.Sync.State, SyncStateSynced)
	}
	if sc.Sync.Error != "" {
		t.Fatalf("sync_error = %q, want empty", sc.Sync.Error)
	}
	if len(fallback.installs) != 0 {
		t.Fatalf("fallback installs = %v, want none", fallback.installs)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands = %v, want 1 command", runner.commands)
	}
	wantPrefix := "schtasks /create /tn " + schtasksScheduleName(sc.ID) + " /xml "
	if !strings.HasPrefix(runner.commands[0], wantPrefix) || !strings.HasSuffix(runner.commands[0], " /f") {
		t.Fatalf("commands[0] = %q, want prefix %q and suffix /f", runner.commands[0], wantPrefix)
	}
	stored := readStoredScheduleRecord(t, store.path)
	sync := decodeRawObject(t, stored["sync"], "stored sync")
	if got := strings.Trim(string(sync["managed_by"]), `"`); got != ManagedBySchTasks {
		t.Fatalf("stored managed_by = %q, want %q", got, ManagedBySchTasks)
	}
	if got := strings.Trim(string(sync["state"]), `"`); got != SyncStateSynced {
		t.Fatalf("stored sync state = %q, want %q", got, SyncStateSynced)
	}
	due, err := store.Due(now.Add(3 * time.Hour))
	if err != nil {
		t.Fatalf("Due(): %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("Due() = %v, want no in-process Task Scheduler schedules", due)
	}
}

func TestAddFallsBackWhenSchTasksCannotRepresentSchedule(t *testing.T) {
	schtasksBackend, runner := testSchTasksBackend(t)
	fallback := &recordingInProcessBackend{}
	store := testStoreWithBackends(t, schtasksBackend, fallback)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.Add(AddInput{
		Trigger: TriggerCalendar,
		Action:  ActionDispatch,
		Cron:    "0 9 * 1,3 1-5",
		Text:    "Run month-filtered weekday summary",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}
	if sc.Sync.ManagedBy != ManagedByInProcess {
		t.Fatalf("managed_by = %q, want %q", sc.Sync.ManagedBy, ManagedByInProcess)
	}
	if sc.Sync.State != SyncStateFallback {
		t.Fatalf("sync_state = %q, want %q", sc.Sync.State, SyncStateFallback)
	}
	if !strings.Contains(sc.Sync.Error, "month and day_of_week") {
		t.Fatalf("sync_error = %q, want month/day_of_week reason", sc.Sync.Error)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("schtasks commands = %v, want none", runner.commands)
	}
	if len(fallback.installs) != 1 || fallback.installs[0] != sc.ID {
		t.Fatalf("fallback installs = %v, want [%s]", fallback.installs, sc.ID)
	}
}

func TestAddFallsBackWhenSchTasksInstallFails(t *testing.T) {
	schtasksBackend, runner := testSchTasksBackend(t)
	fallback := &recordingInProcessBackend{}
	store := testStoreWithBackends(t, schtasksBackend, fallback)
	now := time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))

	sc, err := store.buildSchedule(AddInput{
		Trigger: TriggerInterval,
		Action:  ActionDispatch,
		Every:   "90m",
		Text:    "Run cleanup",
		Now:     now,
	}, now)
	if err != nil {
		t.Fatalf("buildSchedule(): %v", err)
	}
	runner.prefixFailures["schtasks /create /tn "+schtasksScheduleName(sc.ID)+" /xml "] = errors.New("create failed")

	sc, err = store.Add(AddInput{
		Trigger: TriggerInterval,
		Action:  ActionDispatch,
		Every:   "90m",
		Text:    "Run cleanup",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("Add(): %v", err)
	}
	if sc.Sync.ManagedBy != ManagedByInProcess {
		t.Fatalf("managed_by = %q, want %q", sc.Sync.ManagedBy, ManagedByInProcess)
	}
	if sc.Sync.State != SyncStateFallback {
		t.Fatalf("sync_state = %q, want %q", sc.Sync.State, SyncStateFallback)
	}
	if !strings.Contains(sc.Sync.Error, "create failed") {
		t.Fatalf("sync_error = %q, want create failure", sc.Sync.Error)
	}
	if len(fallback.installs) != 1 || fallback.installs[0] != sc.ID {
		t.Fatalf("fallback installs = %v, want [%s]", fallback.installs, sc.ID)
	}
}

func TestMaterializeRemovesExistingNativeScheduleBeforeFallbackAfterUpdateFailure(t *testing.T) {
	native := &recordingNativeBackend{
		name:      ManagedByLaunchd,
		caps:      BackendCaps{NativeInterval: true, MinInterval: time.Minute},
		updateErr: errors.New("update failed"),
	}
	fallback := &recordingInProcessBackend{}
	reconciler := newBackendReconciler(native, fallback)

	sc, err := reconciler.Materialize(Schedule{
		ID:     "sch-update-failure",
		Action: ActionDispatch,
		Spec: ScheduleSpec{
			Trigger:  TriggerInterval,
			Interval: "1h0m0s",
		},
		Sync: ScheduleSync{
			ManagedBy: ManagedByLaunchd,
			State:     SyncStateSynced,
		},
	})
	if err != nil {
		t.Fatalf("Materialize(): %v", err)
	}
	if len(native.updates) != 1 || native.updates[0] != "sch-update-failure" {
		t.Fatalf("native updates = %v, want [sch-update-failure]", native.updates)
	}
	if len(native.removes) != 1 || native.removes[0] != "sch-update-failure" {
		t.Fatalf("native removes = %v, want [sch-update-failure]", native.removes)
	}
	if len(fallback.installs) != 1 || fallback.installs[0] != "sch-update-failure" {
		t.Fatalf("fallback installs = %v, want [sch-update-failure]", fallback.installs)
	}
	if sc.Sync.ManagedBy != ManagedByInProcess {
		t.Fatalf("managed_by = %q, want %q", sc.Sync.ManagedBy, ManagedByInProcess)
	}
	if sc.Sync.State != SyncStateFallback {
		t.Fatalf("sync_state = %q, want %q", sc.Sync.State, SyncStateFallback)
	}
	if !strings.Contains(sc.Sync.Error, "update failed") {
		t.Fatalf("sync_error = %q, want update failure", sc.Sync.Error)
	}
}

func TestMaterializeReturnsErrorWhenNativeCleanupFailsAfterUpdateFailure(t *testing.T) {
	native := &recordingNativeBackend{
		name:      ManagedByLaunchd,
		caps:      BackendCaps{NativeInterval: true, MinInterval: time.Minute},
		updateErr: errors.New("update failed"),
		removeErr: errors.New("cleanup failed"),
	}
	fallback := &recordingInProcessBackend{}
	reconciler := newBackendReconciler(native, fallback)

	_, err := reconciler.Materialize(Schedule{
		ID:     "sch-update-failure",
		Action: ActionDispatch,
		Spec: ScheduleSpec{
			Trigger:  TriggerInterval,
			Interval: "1h0m0s",
		},
		Sync: ScheduleSync{
			ManagedBy: ManagedByLaunchd,
			State:     SyncStateSynced,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cleanup") || !strings.Contains(err.Error(), "update failed") {
		t.Fatalf("Materialize() err = %v, want cleanup + update failure", err)
	}
	if len(fallback.installs) != 0 {
		t.Fatalf("fallback installs = %v, want none", fallback.installs)
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

func TestDueSkipsLaunchdManagedSchedules(t *testing.T) {
	launchdBackend, _, _ := testLaunchdBackend(t)
	fallback := &recordingInProcessBackend{}
	store := testStoreWithBackends(t, launchdBackend, fallback)
	now := time.Date(2026, 3, 18, 10, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
	schedules := []Schedule{
		{
			ID:        "sch-launchd",
			Action:    ActionDispatch,
			Spec:      ScheduleSpec{Trigger: TriggerInterval, Interval: "1h0m0s"},
			Text:      "native",
			CreatedAt: now.Add(-time.Hour),
			NextRun:   now.Add(-time.Minute),
			Sync: ScheduleSync{
				ManagedBy: ManagedByLaunchd,
				State:     SyncStateSynced,
			},
		},
		{
			ID:        "sch-fallback",
			Action:    ActionDispatch,
			Spec:      ScheduleSpec{Trigger: TriggerInterval, Interval: "1h0m0s"},
			Text:      "fallback",
			CreatedAt: now.Add(-time.Hour),
			NextRun:   now.Add(-time.Minute),
			Sync: ScheduleSync{
				ManagedBy: ManagedByInProcess,
				State:     SyncStateFallback,
			},
		},
	}
	if err := store.save(schedules); err != nil {
		t.Fatalf("save schedules: %v", err)
	}
	due, err := store.Due(now)
	if err != nil {
		t.Fatalf("Due(): %v", err)
	}
	if len(due) != 1 || due[0].ID != "sch-fallback" {
		t.Fatalf("Due() = %v, want only sch-fallback", due)
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

func TestDeleteSucceedsWhenSchTasksTaskAlreadyMissing(t *testing.T) {
	schtasksBackend, runner := testSchTasksBackend(t)
	store := testStoreWithBackends(t, schtasksBackend, &recordingInProcessBackend{})
	now := time.Date(2026, 3, 18, 10, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
	sc := Schedule{
		ID:        "sch-missing-native",
		Action:    ActionDispatch,
		Spec:      ScheduleSpec{Trigger: TriggerInterval, Interval: "1h0m0s"},
		Text:      "Run cleanup",
		CreatedAt: now.Add(-time.Hour),
		NextRun:   now.Add(time.Hour),
		Sync: ScheduleSync{
			ManagedBy: ManagedBySchTasks,
			State:     SyncStateSynced,
		},
	}
	if err := store.save([]Schedule{sc}); err != nil {
		t.Fatalf("save schedules: %v", err)
	}
	runner.failures["schtasks /delete /tn "+schtasksScheduleName(sc.ID)+" /f"] = errors.New("ERROR: The system cannot find the file specified.")

	if err := store.Delete(sc.ID); err != nil {
		t.Fatalf("Delete() err = %v, want nil for missing Task Scheduler entry", err)
	}
	schedules, err := store.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(schedules) != 0 {
		t.Fatalf("List() = %v, want schedule removed", schedules)
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
