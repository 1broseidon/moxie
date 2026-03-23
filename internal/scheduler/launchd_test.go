package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLaunchdTriggerSpecTranslatesAtSchedule(t *testing.T) {
	backend := &launchdBackend{
		now: func() time.Time {
			return time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
		},
	}
	sc := Schedule{
		ID: "sch-at",
		Spec: ScheduleSpec{
			Trigger: TriggerAt,
			At:      time.Date(2026, 3, 18, 10, 15, 0, 0, time.FixedZone("CDT", -5*60*60)),
		},
	}

	spec, err := backend.triggerSpec(sc)
	if err != nil {
		t.Fatalf("triggerSpec(): %v", err)
	}
	if spec.startInterval != 0 {
		t.Fatalf("startInterval = %d, want 0", spec.startInterval)
	}
	if len(spec.calendar) != 1 {
		t.Fatalf("calendar len = %d, want 1", len(spec.calendar))
	}
	// The implementation converts At to time.Local, so expected values must match.
	local := sc.Spec.At.In(time.Local)
	want := map[string]int{"Minute": local.Minute(), "Hour": local.Hour(), "Day": local.Day(), "Month": int(local.Month())}
	for key, value := range want {
		if got := spec.calendar[0][key]; got != value {
			t.Fatalf("calendar[%q] = %d, want %d", key, got, value)
		}
	}
}

func TestLaunchdTriggerSpecRejectsAtScheduleWithSeconds(t *testing.T) {
	backend := &launchdBackend{
		now: func() time.Time {
			return time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
		},
	}
	sc := Schedule{
		ID: "sch-at-seconds",
		Spec: ScheduleSpec{
			Trigger: TriggerAt,
			At:      time.Date(2026, 3, 18, 10, 15, 5, 0, time.FixedZone("CDT", -5*60*60)),
		},
	}

	if _, err := backend.triggerSpec(sc); err == nil || !strings.Contains(err.Error(), "minute precision") {
		t.Fatalf("triggerSpec() err = %v, want minute precision error", err)
	}
}

func TestLaunchdTriggerSpecRejectsAtScheduleThatNeedsYearPrecision(t *testing.T) {
	backend := &launchdBackend{
		now: func() time.Time {
			return time.Date(2026, 3, 17, 21, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
		},
	}
	sc := Schedule{
		ID: "sch-at-far-future",
		Spec: ScheduleSpec{
			Trigger: TriggerAt,
			At:      time.Date(2028, 4, 1, 10, 15, 0, 0, time.FixedZone("CDT", -5*60*60)),
		},
	}

	if _, err := backend.triggerSpec(sc); err == nil || !strings.Contains(err.Error(), "year precision") {
		t.Fatalf("triggerSpec() err = %v, want year precision error", err)
	}
}

func TestLaunchdCalendarIntervalsExpandPortableCalendarFields(t *testing.T) {
	calendar, err := normalizeCalendar(&CalendarSpec{
		Minute:     "0",
		Hour:       "9",
		DayOfMonth: "*",
		Month:      "1,3",
		DayOfWeek:  "1-5",
	}, "")
	if err != nil {
		t.Fatalf("normalizeCalendar(): %v", err)
	}

	intervals, err := launchdCalendarIntervals(calendar)
	if err != nil {
		t.Fatalf("launchdCalendarIntervals(): %v", err)
	}
	if len(intervals) != 10 {
		t.Fatalf("interval count = %d, want 10", len(intervals))
	}
	first := intervals[0]
	last := intervals[len(intervals)-1]
	if first["Month"] != 1 || first["Weekday"] != 1 || first["Hour"] != 9 || first["Minute"] != 0 {
		t.Fatalf("first interval = %v", first)
	}
	if last["Month"] != 3 || last["Weekday"] != 5 || last["Hour"] != 9 || last["Minute"] != 0 {
		t.Fatalf("last interval = %v", last)
	}
}

func TestLaunchdSchedulePlistContentsUsesScheduleFireEntrypoint(t *testing.T) {
	spec := launchdTriggerSpec{calendar: []map[string]int{{"Minute": 0, "Hour": 1}}}
	plist, err := launchdSchedulePlistContents("/usr/local/bin/moxie", Schedule{ID: "sch-123"}, spec, launchdScheduleOptions{
		Label:      launchdScheduleLabel("sch-123"),
		WorkingDir: "/tmp/workspace",
		LogPath:    "/tmp/moxie-schedule.log",
		Env: map[string]string{
			"PATH": "/opt/homebrew/bin:/usr/bin:/bin",
			"HOME": "/Users/tester",
		},
	})
	if err != nil {
		t.Fatalf("launchdSchedulePlistContents(): %v", err)
	}
	for _, needle := range []string{
		"<string>/usr/local/bin/moxie</string>",
		"<string>schedule</string>",
		"<string>fire</string>",
		"<string>sch-123</string>",
		"<key>StartCalendarInterval</key>",
		"<key>Minute</key>",
		"<integer>0</integer>",
		"<key>Hour</key>",
		"<integer>1</integer>",
		"<string>/tmp/workspace</string>",
		"<string>/tmp/moxie-schedule.log</string>",
	} {
		if !strings.Contains(plist, needle) {
			t.Fatalf("plist missing %q: %s", needle, plist)
		}
	}
}

func TestLaunchdRemoveSucceedsWhenBootoutFails(t *testing.T) {
	dir := t.TempDir()
	plistDir := filepath.Join(dir, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o755); err != nil {
		t.Fatal(err)
	}
	schedID := "sch-oneshot-1"
	plistPath := launchdSchedulePlistPath(dir, schedID)
	if err := os.WriteFile(plistPath, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	backend := &launchdBackend{
		homeDir: func() (string, error) { return dir, nil },
		uid:     func() int { return 501 },
		runCommand: func(name string, args ...string) ([]byte, error) {
			if name == "launchctl" && len(args) > 0 && args[0] == "print" {
				return []byte("service info"), nil
			}
			if name == "launchctl" && len(args) > 0 && args[0] == "bootout" {
				return nil, fmt.Errorf("exit status 3")
			}
			return nil, nil
		},
	}

	if err := backend.Remove(schedID); err != nil {
		t.Fatalf("Remove() should succeed even when bootout fails: %v", err)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatal("plist file should be removed after Remove()")
	}
}
