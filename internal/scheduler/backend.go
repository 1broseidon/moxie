package scheduler

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

const (
	ManagedByInProcess = "in-process"
	ManagedByCron      = "cron"
	ManagedByLaunchd   = "launchd"
	ManagedBySchTasks  = "schtasks"

	SyncStateSynced   = "synced"
	SyncStatePending  = "pending"
	SyncStateFallback = "fallback"
	SyncStateError    = "error"
)

type ScheduleBackend interface {
	Name() string
	Install(Schedule) error
	Update(Schedule) error
	Remove(id string) error
	Supports() BackendCaps
}

type BackendCaps struct {
	NativeAt       bool
	NativeInterval bool
	NativeCalendar bool
	MinInterval    time.Duration
}

type scheduleSupportChecker interface {
	SupportsSchedule(Schedule) bool
}

type scheduleSupportExplainer interface {
	SupportError(Schedule) error
}

type backendReconciler struct {
	native   []ScheduleBackend
	fallback ScheduleBackend
	byName   map[string]ScheduleBackend
}

type inProcessBackend struct{}

func defaultBackendReconciler() *backendReconciler {
	backends := []ScheduleBackend{inProcessBackend{}}
	if runtime.GOOS == "darwin" {
		backends = append([]ScheduleBackend{newLaunchdBackend()}, backends...)
	}
	return newBackendReconciler(backends...)
}

func newBackendReconciler(backends ...ScheduleBackend) *backendReconciler {
	r := &backendReconciler{byName: map[string]ScheduleBackend{}}
	for _, backend := range backends {
		if backend == nil {
			continue
		}
		name := strings.TrimSpace(backend.Name())
		if name == "" {
			continue
		}
		r.byName[name] = backend
		if name == ManagedByInProcess {
			r.fallback = backend
			continue
		}
		r.native = append(r.native, backend)
	}
	if r.fallback == nil {
		r.fallback = inProcessBackend{}
		r.byName[r.fallback.Name()] = r.fallback
	}
	return r
}

func (inProcessBackend) Name() string {
	return ManagedByInProcess
}

func (inProcessBackend) Install(Schedule) error {
	return nil
}

func (inProcessBackend) Update(Schedule) error {
	return nil
}

func (inProcessBackend) Remove(string) error {
	return nil
}

func (inProcessBackend) Supports() BackendCaps {
	return BackendCaps{}
}

func (r *backendReconciler) Normalize(sc Schedule) (Schedule, error) {
	next := sc
	next.Sync.ManagedBy = strings.TrimSpace(next.Sync.ManagedBy)
	next.Sync.State = strings.TrimSpace(next.Sync.State)
	next.Sync.Error = strings.TrimSpace(next.Sync.Error)

	backend := r.backendForManagedBy(next.Sync.ManagedBy)
	if backend == nil {
		backend = r.fallback
		next.Sync.ManagedBy = backend.Name()
		next.Sync.State = ""
	}
	if next.Sync.State == "" {
		next.Sync.State = r.defaultSyncState(backend)
	}
	return next, nil
}

func (r *backendReconciler) Materialize(sc Schedule) (Schedule, error) {
	target, supportErr := r.selectBackend(sc)
	next := sc
	next.Sync.Error = ""

	existing := r.backendForManagedBy(sc.Sync.ManagedBy)
	var err error
	switch {
	case existing != nil && existing.Name() == target.Name():
		err = target.Update(sc)
	case existing != nil && existing.Name() != target.Name():
		if removeErr := existing.Remove(sc.ID); removeErr != nil {
			return Schedule{}, fmt.Errorf("remove schedule %s from %s: %w", sc.ID, existing.Name(), removeErr)
		}
		err = target.Install(sc)
	default:
		err = target.Install(sc)
	}
	if err == nil {
		next.Sync.ManagedBy = target.Name()
		next.Sync.State = r.defaultSyncState(target)
		if target.Name() == r.fallback.Name() && supportErr != nil {
			next.Sync.Error = supportErr.Error()
		}
		return next, nil
	}
	if target.Name() == r.fallback.Name() {
		if supportErr != nil {
			return Schedule{}, fmt.Errorf("materialize schedule %s via %s after native fallback reason %q: %w", sc.ID, target.Name(), supportErr.Error(), err)
		}
		return Schedule{}, fmt.Errorf("materialize schedule %s via %s: %w", sc.ID, target.Name(), err)
	}
	if existing != nil && existing.Name() == target.Name() {
		if removeErr := existing.Remove(sc.ID); removeErr != nil {
			return Schedule{}, fmt.Errorf("materialize schedule %s via %s: %w (cleanup %s: %v)", sc.ID, target.Name(), err, existing.Name(), removeErr)
		}
	}
	if fallbackErr := r.fallback.Install(sc); fallbackErr != nil {
		return Schedule{}, fmt.Errorf("materialize schedule %s via %s: %w (fallback %s: %v)", sc.ID, target.Name(), err, r.fallback.Name(), fallbackErr)
	}
	next.Sync.ManagedBy = r.fallback.Name()
	next.Sync.State = SyncStateFallback
	next.Sync.Error = err.Error()
	return next, nil
}

func (r *backendReconciler) Remove(sc Schedule) error {
	backend := r.backendForManagedBy(sc.Sync.ManagedBy)
	if backend == nil {
		backend, _ = r.selectBackend(sc)
	}
	return backend.Remove(sc.ID)
}

func (r *backendReconciler) selectBackend(sc Schedule) (ScheduleBackend, error) {
	var supportErr error
	for _, backend := range r.native {
		supported, err := backendSupportsSchedule(backend, sc)
		if supported {
			return backend, nil
		}
		if supportErr == nil && err != nil {
			supportErr = err
		}
	}
	return r.fallback, supportErr
}

func (r *backendReconciler) backendForManagedBy(name string) ScheduleBackend {
	if r == nil {
		return nil
	}
	return r.byName[strings.TrimSpace(name)]
}

func (r *backendReconciler) defaultSyncState(backend ScheduleBackend) string {
	if backend != nil && backend.Name() != r.fallback.Name() {
		return SyncStateSynced
	}
	return SyncStateFallback
}

func backendSupportsSchedule(backend ScheduleBackend, sc Schedule) (bool, error) {
	caps := backend.Supports()
	supportsTrigger := false
	switch canonicalTrigger(sc.Spec.Trigger) {
	case TriggerAt:
		supportsTrigger = caps.NativeAt
	case TriggerInterval:
		if !caps.NativeInterval {
			return false, fmt.Errorf("%s does not support interval schedules", backend.Name())
		}
		if caps.MinInterval <= 0 {
			supportsTrigger = true
			break
		}
		d, err := parseEvery(sc.Spec.Interval)
		if err != nil {
			return false, err
		}
		if d < caps.MinInterval {
			return false, fmt.Errorf("%s interval schedules require at least %s", backend.Name(), caps.MinInterval)
		}
		supportsTrigger = true
	case TriggerCalendar:
		supportsTrigger = caps.NativeCalendar
	default:
		return false, fmt.Errorf("%s does not support trigger %q", backend.Name(), sc.Spec.Trigger)
	}
	if !supportsTrigger {
		return false, fmt.Errorf("%s does not support %s schedules", backend.Name(), canonicalTrigger(sc.Spec.Trigger))
	}
	checker, ok := backend.(scheduleSupportChecker)
	if !ok {
		return true, nil
	}
	if checker.SupportsSchedule(sc) {
		return true, nil
	}
	explainer, ok := backend.(scheduleSupportExplainer)
	if !ok {
		return false, nil
	}
	return false, explainer.SupportError(sc)
}

func FireCommand(id string) []string {
	return []string{"schedule", "fire", id}
}
