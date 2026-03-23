package scheduler

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/1broseidon/moxie/internal/store"
)

const (
	schtasksScheduleNamePrefix = "io.github.1broseidon.moxie.schedule."
	schtasksMaxInterval        = 31 * 24 * time.Hour
	schtasksMaxTriggers        = 512
)

type schtasksBackend struct {
	binaryPath  func() (string, error)
	currentUser func() (string, error)
	now         func() time.Time
	runCommand  func(name string, args ...string) ([]byte, error)
}

type schtasksTaskSpec struct {
	triggers []schtasksTriggerSpec
}

type schtasksTriggerSpec struct {
	kind          string
	startBoundary time.Time
	repetition    *schtasksRepetitionSpec
	schedule      *schtasksCalendarScheduleSpec
}

type schtasksRepetitionSpec struct {
	interval string
	duration string
}

type schtasksCalendarScheduleSpec struct {
	kind          string
	daysInterval  int
	weeksInterval int
	daysOfWeek    []int
	daysOfMonth   []int
	months        []int
}

type schtasksScheduleOptions struct {
	Author      string
	WorkingDir  string
	Description string
}

func newSchTasksBackend() ScheduleBackend {
	return &schtasksBackend{
		binaryPath:  resolveScheduleBinaryPath,
		currentUser: resolveCurrentUser,
		now:         time.Now,
		runCommand:  runCombinedCommand,
	}
}

func (*schtasksBackend) Name() string {
	return ManagedBySchTasks
}

func (*schtasksBackend) Supports() BackendCaps {
	return BackendCaps{
		NativeAt:       true,
		NativeInterval: true,
		NativeCalendar: true,
		MinInterval:    time.Minute,
	}
}

func (b *schtasksBackend) SupportsSchedule(sc Schedule) bool {
	_, err := b.taskSpec(sc)
	return err == nil
}

func (b *schtasksBackend) SupportError(sc Schedule) error {
	_, err := b.taskSpec(sc)
	return err
}

func (b *schtasksBackend) Install(sc Schedule) error {
	return b.installOrUpdate(sc)
}

func (b *schtasksBackend) Update(sc Schedule) error {
	return b.installOrUpdate(sc)
}

func (b *schtasksBackend) Remove(id string) error {
	output, err := b.runCommand("schtasks", "/delete", "/tn", schtasksScheduleName(id), "/f")
	if err != nil {
		if schtasksMissingTask(output, err) {
			return nil
		}
		return fmt.Errorf("schtasks /delete %s: %w", schtasksScheduleName(id), err)
	}
	return nil
}

func (b *schtasksBackend) installOrUpdate(sc Schedule) error {
	spec, err := b.taskSpec(sc)
	if err != nil {
		return err
	}
	binaryPath, err := b.binaryPath()
	if err != nil {
		return err
	}
	currentUser, err := b.currentUser()
	if err != nil {
		return err
	}
	workingDir, err := schtasksScheduleWorkingDirectory()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return err
	}
	content, err := schtasksTaskXML(binaryPath, sc, spec, schtasksScheduleOptions{
		Author:      currentUser,
		WorkingDir:  workingDir,
		Description: "Moxie schedule " + sc.ID,
	})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "moxie-schtasks-*.xml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if _, err := tmp.WriteString(content); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if _, err := b.runCommand("schtasks", "/create", "/tn", schtasksScheduleName(sc.ID), "/xml", tmpPath, "/f"); err != nil {
		return fmt.Errorf("schtasks /create %s: %w", schtasksScheduleName(sc.ID), err)
	}
	return nil
}

func (b *schtasksBackend) taskSpec(sc Schedule) (schtasksTaskSpec, error) {
	loc := scheduleLocation(sc)
	switch canonicalTrigger(sc.Spec.Trigger) {
	case TriggerAt:
		if sc.Spec.At.IsZero() {
			return schtasksTaskSpec{}, fmt.Errorf("Task Scheduler one-shot schedules require an at timestamp")
		}
		return schtasksTaskSpec{
			triggers: []schtasksTriggerSpec{{
				kind:          "TimeTrigger",
				startBoundary: sc.Spec.At.In(loc),
			}},
		}, nil
	case TriggerInterval:
		d, err := parseEvery(sc.Spec.Interval)
		if err != nil {
			return schtasksTaskSpec{}, err
		}
		if d > schtasksMaxInterval {
			return schtasksTaskSpec{}, fmt.Errorf("Task Scheduler interval schedules require at most %s", schtasksMaxInterval)
		}
		start := sc.NextRun
		if start.IsZero() {
			start = b.currentTime().In(loc).Add(d)
		}
		return schtasksTaskSpec{
			triggers: []schtasksTriggerSpec{{
				kind:          "TimeTrigger",
				startBoundary: start.In(loc),
				repetition: &schtasksRepetitionSpec{
					interval: taskSchedulerDuration(d),
				},
			}},
		}, nil
	case TriggerCalendar:
		calendar, err := normalizeCalendar(sc.Spec.Calendar, sc.Spec.legacyCronSpec())
		if err != nil {
			return schtasksTaskSpec{}, err
		}
		triggers, err := b.calendarTriggers(calendar, loc)
		if err != nil {
			return schtasksTaskSpec{}, err
		}
		return schtasksTaskSpec{triggers: triggers}, nil
	default:
		return schtasksTaskSpec{}, fmt.Errorf("Task Scheduler does not support trigger %q", sc.Spec.Trigger)
	}
}

func (b *schtasksBackend) currentTime() time.Time {
	if b != nil && b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *schtasksBackend) calendarTriggers(calendar *CalendarSpec, loc *time.Location) ([]schtasksTriggerSpec, error) {
	if calendar == nil {
		return nil, fmt.Errorf("calendar spec cannot be empty")
	}
	hours, minutes, err := schtasksCalendarTimes(calendar)
	if err != nil {
		return nil, err
	}
	schedule, err := schtasksCalendarPattern(calendar)
	if err != nil {
		return nil, err
	}

	reference := b.currentTime()
	if loc != nil {
		reference = reference.In(loc)
	}
	triggers := make([]schtasksTriggerSpec, 0, len(minutes)*len(hours))
	for _, hour := range hours {
		for _, minute := range minutes {
			spec := *calendar
			spec.Hour = strconv.Itoa(hour)
			spec.Minute = strconv.Itoa(minute)
			startBoundary, err := nextCalendarRun(&spec, reference, loc)
			if err != nil {
				return nil, err
			}
			triggers = append(triggers, schtasksTriggerSpec{
				kind:          "CalendarTrigger",
				startBoundary: startBoundary,
				schedule:      schedule,
			})
			if len(triggers) > schtasksMaxTriggers {
				return nil, fmt.Errorf("calendar schedule expands to too many Task Scheduler triggers")
			}
		}
	}
	return triggers, nil
}

func schtasksCalendarTimes(calendar *CalendarSpec) ([]int, []int, error) {
	minutes, minutesWildcard, err := expandLaunchdCalendarField(calendar.Minute, calendarMinuteField)
	if err != nil {
		return nil, nil, err
	}
	hours, hoursWildcard, err := expandLaunchdCalendarField(calendar.Hour, calendarHourField)
	if err != nil {
		return nil, nil, err
	}
	if minutesWildcard || hoursWildcard || len(minutes) == 0 || len(hours) == 0 {
		return nil, nil, fmt.Errorf("Task Scheduler calendar schedules require explicit minute and hour values")
	}
	return hours, minutes, nil
}

func schtasksCalendarPattern(calendar *CalendarSpec) (*schtasksCalendarScheduleSpec, error) {
	daysOfMonth, domWildcard, err := expandLaunchdCalendarField(calendar.DayOfMonth, calendarDOMField)
	if err != nil {
		return nil, err
	}
	months, monthWildcard, err := expandLaunchdCalendarField(calendar.Month, calendarMonthField)
	if err != nil {
		return nil, err
	}
	daysOfWeek, dowWildcard, err := expandLaunchdCalendarField(calendar.DayOfWeek, calendarDOWField)
	if err != nil {
		return nil, err
	}
	return schtasksCalendarSchedule(daysOfMonth, domWildcard, months, monthWildcard, daysOfWeek, dowWildcard)
}

func schtasksCalendarSchedule(daysOfMonth []int, domWildcard bool, months []int, monthWildcard bool, daysOfWeek []int, dowWildcard bool) (*schtasksCalendarScheduleSpec, error) {
	switch {
	case !dowWildcard && !monthWildcard:
		return nil, fmt.Errorf("Task Scheduler cannot combine month and day_of_week constraints in portable calendar mode")
	case !dowWildcard:
		return &schtasksCalendarScheduleSpec{
			kind:          "week",
			weeksInterval: 1,
			daysOfWeek:    append([]int(nil), daysOfWeek...),
		}, nil
	case domWildcard && monthWildcard:
		return &schtasksCalendarScheduleSpec{
			kind:         "day",
			daysInterval: 1,
		}, nil
	default:
		if domWildcard {
			daysOfMonth = calendarRange(calendarDOMField.min, calendarDOMField.max)
		}
		if monthWildcard {
			months = calendarRange(calendarMonthField.min, calendarMonthField.max)
		}
		return &schtasksCalendarScheduleSpec{
			kind:        "month",
			daysOfMonth: append([]int(nil), daysOfMonth...),
			months:      append([]int(nil), months...),
		}, nil
	}
}

func schtasksTaskXML(binaryPath string, sc Schedule, spec schtasksTaskSpec, opts schtasksScheduleOptions) (string, error) {
	if strings.TrimSpace(binaryPath) == "" {
		return "", fmt.Errorf("Task Scheduler binary path cannot be empty")
	}
	if strings.TrimSpace(opts.Author) == "" {
		return "", fmt.Errorf("Task Scheduler author cannot be empty")
	}
	if strings.TrimSpace(opts.WorkingDir) == "" {
		return "", fmt.Errorf("Task Scheduler working directory cannot be empty")
	}
	if len(spec.triggers) == 0 {
		return "", fmt.Errorf("Task Scheduler task must define at least one trigger")
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` + "\n")
	b.WriteString("  <RegistrationInfo>\n")
	b.WriteString("    <Date>" + xmlEscape(taskSchedulerTimestamp(sc.CreatedAt)) + "</Date>\n")
	b.WriteString("    <Author>" + xmlEscape(opts.Author) + "</Author>\n")
	if desc := strings.TrimSpace(opts.Description); desc != "" {
		b.WriteString("    <Description>" + xmlEscape(desc) + "</Description>\n")
	}
	b.WriteString("  </RegistrationInfo>\n")
	b.WriteString("  <Triggers>\n")
	for _, trigger := range spec.triggers {
		writeSchTasksTrigger(&b, trigger, "    ")
	}
	b.WriteString("  </Triggers>\n")
	b.WriteString("  <Principals>\n")
	b.WriteString("    <Principal id=\"Author\">\n")
	b.WriteString("      <UserId>" + xmlEscape(opts.Author) + "</UserId>\n")
	b.WriteString("      <LogonType>InteractiveToken</LogonType>\n")
	b.WriteString("      <RunLevel>LeastPrivilege</RunLevel>\n")
	b.WriteString("    </Principal>\n")
	b.WriteString("  </Principals>\n")
	b.WriteString("  <Settings>\n")
	b.WriteString("    <Enabled>true</Enabled>\n")
	b.WriteString("    <AllowStartOnDemand>true</AllowStartOnDemand>\n")
	b.WriteString("    <StartWhenAvailable>true</StartWhenAvailable>\n")
	b.WriteString("  </Settings>\n")
	b.WriteString("  <Actions Context=\"Author\">\n")
	b.WriteString("    <Exec>\n")
	b.WriteString("      <Command>" + xmlEscape(binaryPath) + "</Command>\n")
	b.WriteString("      <Arguments>" + xmlEscape(strings.Join(FireCommand(sc.ID), " ")) + "</Arguments>\n")
	b.WriteString("      <WorkingDirectory>" + xmlEscape(opts.WorkingDir) + "</WorkingDirectory>\n")
	b.WriteString("    </Exec>\n")
	b.WriteString("  </Actions>\n")
	b.WriteString("</Task>\n")
	return b.String(), nil
}

func writeSchTasksTrigger(b *strings.Builder, trigger schtasksTriggerSpec, indent string) {
	b.WriteString(indent + "<" + trigger.kind + ">\n")
	b.WriteString(indent + "  <StartBoundary>" + xmlEscape(taskSchedulerTimestamp(trigger.startBoundary)) + "</StartBoundary>\n")
	if trigger.repetition != nil {
		b.WriteString(indent + "  <Repetition>\n")
		b.WriteString(indent + "    <Interval>" + xmlEscape(trigger.repetition.interval) + "</Interval>\n")
		if duration := strings.TrimSpace(trigger.repetition.duration); duration != "" {
			b.WriteString(indent + "    <Duration>" + xmlEscape(duration) + "</Duration>\n")
		}
		b.WriteString(indent + "  </Repetition>\n")
	}
	if trigger.schedule != nil {
		writeSchTasksCalendarSchedule(b, *trigger.schedule, indent+"  ")
	}
	b.WriteString(indent + "  <Enabled>true</Enabled>\n")
	b.WriteString(indent + "</" + trigger.kind + ">\n")
}

func writeSchTasksCalendarSchedule(b *strings.Builder, schedule schtasksCalendarScheduleSpec, indent string) {
	switch schedule.kind {
	case "day":
		b.WriteString(indent + "<ScheduleByDay>\n")
		b.WriteString(indent + "  <DaysInterval>" + strconv.Itoa(schedule.daysInterval) + "</DaysInterval>\n")
		b.WriteString(indent + "</ScheduleByDay>\n")
	case "week":
		b.WriteString(indent + "<ScheduleByWeek>\n")
		b.WriteString(indent + "  <WeeksInterval>" + strconv.Itoa(schedule.weeksInterval) + "</WeeksInterval>\n")
		b.WriteString(indent + "  <DaysOfWeek>\n")
		for _, day := range schedule.daysOfWeek {
			b.WriteString(indent + "    <" + taskSchedulerDayName(day) + "/>\n")
		}
		b.WriteString(indent + "  </DaysOfWeek>\n")
		b.WriteString(indent + "</ScheduleByWeek>\n")
	case "month":
		b.WriteString(indent + "<ScheduleByMonth>\n")
		b.WriteString(indent + "  <DaysOfMonth>\n")
		for _, day := range schedule.daysOfMonth {
			b.WriteString(indent + "    <Day>" + strconv.Itoa(day) + "</Day>\n")
		}
		b.WriteString(indent + "  </DaysOfMonth>\n")
		b.WriteString(indent + "  <Months>\n")
		for _, month := range schedule.months {
			b.WriteString(indent + "    <" + taskSchedulerMonthName(month) + "/>\n")
		}
		b.WriteString(indent + "  </Months>\n")
		b.WriteString(indent + "</ScheduleByMonth>\n")
	default:
		panic("unsupported Task Scheduler calendar kind: " + schedule.kind)
	}
}

func schtasksScheduleName(id string) string {
	return schtasksScheduleNamePrefix + strings.TrimSpace(id)
}

func schtasksScheduleWorkingDirectory() (string, error) {
	if cfg, err := store.LoadConfig(); err == nil {
		if trimmed := strings.TrimSpace(cfg.DefaultCWD); trimmed != "" {
			return trimmed, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	base := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if base == "" {
		base = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(base, "Moxie", "workspace"), nil
}

func scheduleLocation(sc Schedule) *time.Location {
	switch {
	case !sc.NextRun.IsZero() && sc.NextRun.Location() != nil:
		return sc.NextRun.Location()
	case !sc.Spec.At.IsZero() && sc.Spec.At.Location() != nil:
		return sc.Spec.At.Location()
	default:
		return time.Local
	}
}

func taskSchedulerTimestamp(ts time.Time) string {
	if ts.IsZero() {
		ts = time.Now()
	}
	return ts.Format(time.RFC3339)
}

func taskSchedulerDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	totalSeconds := int64(d / time.Second)
	days := totalSeconds / int64(24*time.Hour/time.Second)
	totalSeconds %= int64(24 * time.Hour / time.Second)
	hours := totalSeconds / int64(time.Hour/time.Second)
	totalSeconds %= int64(time.Hour / time.Second)
	minutes := totalSeconds / int64(time.Minute/time.Second)
	seconds := totalSeconds % int64(time.Minute/time.Second)

	var b strings.Builder
	b.WriteString("P")
	if days > 0 {
		b.WriteString(strconv.FormatInt(days, 10))
		b.WriteString("D")
	}
	if hours > 0 || minutes > 0 || seconds > 0 || days == 0 {
		b.WriteString("T")
		if hours > 0 {
			b.WriteString(strconv.FormatInt(hours, 10))
			b.WriteString("H")
		}
		if minutes > 0 {
			b.WriteString(strconv.FormatInt(minutes, 10))
			b.WriteString("M")
		}
		if seconds > 0 || (hours == 0 && minutes == 0) {
			b.WriteString(strconv.FormatInt(seconds, 10))
			b.WriteString("S")
		}
	}
	return b.String()
}

func taskSchedulerDayName(day int) string {
	switch day {
	case 0:
		return "Sunday"
	case 1:
		return "Monday"
	case 2:
		return "Tuesday"
	case 3:
		return "Wednesday"
	case 4:
		return "Thursday"
	case 5:
		return "Friday"
	case 6:
		return "Saturday"
	default:
		panic(fmt.Sprintf("unsupported day_of_week %d", day))
	}
}

func taskSchedulerMonthName(month int) string {
	switch month {
	case 1:
		return "January"
	case 2:
		return "February"
	case 3:
		return "March"
	case 4:
		return "April"
	case 5:
		return "May"
	case 6:
		return "June"
	case 7:
		return "July"
	case 8:
		return "August"
	case 9:
		return "September"
	case 10:
		return "October"
	case 11:
		return "November"
	case 12:
		return "December"
	default:
		panic(fmt.Sprintf("unsupported month %d", month))
	}
}

func schtasksMissingTask(output []byte, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(string(output)))
	if text == "" {
		text = strings.ToLower(err.Error())
	} else {
		text += "\n" + strings.ToLower(err.Error())
	}
	return strings.Contains(text, "the system cannot find the file specified") ||
		strings.Contains(text, "cannot find the task") ||
		strings.Contains(text, "path specified") && strings.Contains(text, "cannot find")
}

func calendarRange(min, max int) []int {
	values := make([]int, 0, max-min+1)
	for value := min; value <= max; value++ {
		values = append(values, value)
	}
	return values
}

func resolveScheduleBinaryPath() (string, error) {
	if path, err := exec.LookPath("moxie"); err == nil && strings.TrimSpace(path) != "" {
		if filepath.IsAbs(path) {
			return path, nil
		}
		abs, absErr := filepath.Abs(path)
		if absErr == nil {
			return abs, nil
		}
		return path, nil
	}
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return "", fmt.Errorf("failed to locate moxie binary: %w", err)
	}
	return exe, nil
}

func resolveCurrentUser() (string, error) {
	if current, err := user.Current(); err == nil && strings.TrimSpace(current.Username) != "" {
		return current.Username, nil
	}
	for _, key := range []string{"USERNAME", "USER"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("failed to resolve current user")
}

func runCombinedCommand(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	return cmd.CombinedOutput()
}
