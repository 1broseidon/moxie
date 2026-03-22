package scheduler

import "time"

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

func FireCommand(id string) []string {
	return []string{"schedule", "fire", id}
}
