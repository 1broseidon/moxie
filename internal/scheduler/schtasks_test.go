package scheduler

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testSchTasksTranslator() *schtasksBackend {
	return &schtasksBackend{
		now: func() time.Time {
			return time.Date(2026, 3, 17, 8, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
		},
	}
}

func TestSchTasksTaskSpecTranslatesAtSchedule(t *testing.T) {
	backend := testSchTasksTranslator()
	when := time.Date(2026, 3, 18, 10, 15, 5, 0, time.FixedZone("CDT", -5*60*60))
	sc := Schedule{
		ID: "sch-at",
		Spec: ScheduleSpec{
			Trigger: TriggerAt,
			At:      when,
		},
	}

	spec, err := backend.taskSpec(sc)
	if err != nil {
		t.Fatalf("taskSpec(): %v", err)
	}
	if len(spec.triggers) != 1 {
		t.Fatalf("trigger count = %d, want 1", len(spec.triggers))
	}
	trigger := spec.triggers[0]
	if trigger.kind != "TimeTrigger" {
		t.Fatalf("trigger kind = %q, want TimeTrigger", trigger.kind)
	}
	if !trigger.startBoundary.Equal(when) {
		t.Fatalf("startBoundary = %v, want %v", trigger.startBoundary, when)
	}
	if trigger.repetition != nil {
		t.Fatalf("repetition = %+v, want nil", trigger.repetition)
	}
}

func TestSchTasksTaskSpecTranslatesIntervalSchedule(t *testing.T) {
	backend := testSchTasksTranslator()
	nextRun := time.Date(2026, 3, 17, 9, 30, 0, 0, time.FixedZone("CDT", -5*60*60))
	sc := Schedule{
		ID:      "sch-interval",
		NextRun: nextRun,
		Spec: ScheduleSpec{
			Trigger:  TriggerInterval,
			Interval: "90m",
		},
	}

	spec, err := backend.taskSpec(sc)
	if err != nil {
		t.Fatalf("taskSpec(): %v", err)
	}
	if len(spec.triggers) != 1 {
		t.Fatalf("trigger count = %d, want 1", len(spec.triggers))
	}
	trigger := spec.triggers[0]
	if trigger.kind != "TimeTrigger" {
		t.Fatalf("trigger kind = %q, want TimeTrigger", trigger.kind)
	}
	if !trigger.startBoundary.Equal(nextRun) {
		t.Fatalf("startBoundary = %v, want %v", trigger.startBoundary, nextRun)
	}
	if trigger.repetition == nil || trigger.repetition.interval != "PT1H30M" {
		t.Fatalf("repetition = %+v, want PT1H30M", trigger.repetition)
	}
}

func TestSchTasksTaskSpecRejectsLongInterval(t *testing.T) {
	backend := testSchTasksTranslator()
	sc := Schedule{
		ID: "sch-long-interval",
		Spec: ScheduleSpec{
			Trigger:  TriggerInterval,
			Interval: "32d",
		},
	}

	if _, err := backend.taskSpec(sc); err == nil || !strings.Contains(err.Error(), "at most 744h0m0s") {
		t.Fatalf("taskSpec() err = %v, want max interval error", err)
	}
}

func TestSchTasksCalendarTriggersExpandConcreteTimes(t *testing.T) {
	backend := testSchTasksTranslator()
	calendar, err := normalizeCalendar(&CalendarSpec{
		Minute:     "0,30",
		Hour:       "9-10",
		DayOfMonth: "*",
		Month:      "*",
		DayOfWeek:  "1-5",
	}, "")
	if err != nil {
		t.Fatalf("normalizeCalendar(): %v", err)
	}

	triggers, err := backend.calendarTriggers(calendar, time.FixedZone("CDT", -5*60*60))
	if err != nil {
		t.Fatalf("calendarTriggers(): %v", err)
	}
	if len(triggers) != 4 {
		t.Fatalf("trigger count = %d, want 4", len(triggers))
	}
	if triggers[0].schedule == nil || triggers[0].schedule.kind != "week" {
		t.Fatalf("schedule = %+v, want weekly schedule", triggers[0].schedule)
	}
	if got := triggers[0].schedule.daysOfWeek; len(got) != 5 || got[0] != 1 || got[4] != 5 {
		t.Fatalf("daysOfWeek = %v, want [1 2 3 4 5]", got)
	}
	wantStarts := []time.Time{
		time.Date(2026, 3, 17, 9, 0, 0, 0, time.FixedZone("CDT", -5*60*60)),
		time.Date(2026, 3, 17, 9, 30, 0, 0, time.FixedZone("CDT", -5*60*60)),
		time.Date(2026, 3, 17, 10, 0, 0, 0, time.FixedZone("CDT", -5*60*60)),
		time.Date(2026, 3, 17, 10, 30, 0, 0, time.FixedZone("CDT", -5*60*60)),
	}
	for i, want := range wantStarts {
		if !triggers[i].startBoundary.Equal(want) {
			t.Fatalf("trigger[%d].startBoundary = %v, want %v", i, triggers[i].startBoundary, want)
		}
	}
}

func TestSchTasksCalendarTriggersRejectMonthFilteredWeekdaySchedule(t *testing.T) {
	backend := testSchTasksTranslator()
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

	if _, err := backend.calendarTriggers(calendar, time.FixedZone("CDT", -5*60*60)); err == nil || !strings.Contains(err.Error(), "month and day_of_week") {
		t.Fatalf("calendarTriggers() err = %v, want month/day_of_week error", err)
	}
}

func TestSchTasksTaskXMLUsesScheduleFireEntrypoint(t *testing.T) {
	xml, err := schtasksTaskXML(`C:\Program Files\Moxie\moxie.exe`, Schedule{
		ID:        "sch-123",
		CreatedAt: time.Date(2026, 3, 17, 8, 0, 0, 0, time.FixedZone("CDT", -5*60*60)),
	}, schtasksTaskSpec{
		triggers: []schtasksTriggerSpec{{
			kind:          "TimeTrigger",
			startBoundary: time.Date(2026, 3, 18, 9, 30, 0, 0, time.FixedZone("CDT", -5*60*60)),
			repetition: &schtasksRepetitionSpec{
				interval: "PT1H30M",
			},
		}},
	}, schtasksScheduleOptions{
		Author:      `DOMAIN\tester`,
		WorkingDir:  `C:\Users\tester\AppData\Local\Moxie\workspace`,
		Description: "Moxie schedule sch-123",
	})
	if err != nil {
		t.Fatalf("schtasksTaskXML(): %v", err)
	}
	for _, needle := range []string{
		`<Command>C:\Program Files\Moxie\moxie.exe</Command>`,
		`<Arguments>schedule fire sch-123</Arguments>`,
		`<WorkingDirectory>C:\Users\tester\AppData\Local\Moxie\workspace</WorkingDirectory>`,
		`<LogonType>InteractiveToken</LogonType>`,
		`<RunLevel>LeastPrivilege</RunLevel>`,
		`<TimeTrigger>`,
		`<Interval>PT1H30M</Interval>`,
	} {
		if !strings.Contains(xml, needle) {
			t.Fatalf("xml missing %q: %s", needle, xml)
		}
	}
}

func TestSchTasksRemoveIgnoresMissingTask(t *testing.T) {
	runner := &recordingCommandRunner{failures: map[string]error{}, prefixFailures: map[string]error{}}
	backend := &schtasksBackend{runCommand: runner.run}
	command := "schtasks /delete /tn " + schtasksScheduleName("sch-missing") + " /f"
	runner.failures[command] = errors.New("ERROR: The system cannot find the file specified.")

	if err := backend.Remove("sch-missing"); err != nil {
		t.Fatalf("Remove() err = %v, want nil for missing task", err)
	}
}

func TestSchTasksRemoveReturnsUnexpectedDeleteFailure(t *testing.T) {
	runner := &recordingCommandRunner{failures: map[string]error{}, prefixFailures: map[string]error{}}
	backend := &schtasksBackend{runCommand: runner.run}
	command := "schtasks /delete /tn " + schtasksScheduleName("sch-protected") + " /f"
	runner.failures[command] = errors.New("ERROR: Access is denied.")

	if err := backend.Remove("sch-protected"); err == nil || !strings.Contains(err.Error(), "Access is denied") {
		t.Fatalf("Remove() err = %v, want access denied failure", err)
	}
}
