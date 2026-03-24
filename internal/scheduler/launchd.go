package scheduler

import (
	"encoding/xml"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/1broseidon/moxie/internal/store"
)

const (
	launchdScheduleLabelPrefix  = "io.github.1broseidon.moxie.schedule."
	launchdScheduleDefaultPATH  = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	launchdScheduleMaxIntervals = 512
)

type launchdBackend struct {
	homeDir    func() (string, error)
	binaryPath func() (string, error)
	now        func() time.Time
	uid        func() int
	env        func() map[string]string
	runCommand func(name string, args ...string) ([]byte, error)
}

type launchdTriggerSpec struct {
	startInterval int
	calendar      []map[string]int
}

func newLaunchdBackend() ScheduleBackend {
	return &launchdBackend{
		homeDir:    os.UserHomeDir,
		binaryPath: resolveScheduleBinaryPath,
		now:        time.Now,
		uid:        os.Getuid,
		env:        launchdScheduleEnvironment,
		runCommand: runCombinedCommand,
	}
}

func (*launchdBackend) Name() string {
	return ManagedByLaunchd
}

func (*launchdBackend) Supports() BackendCaps {
	return BackendCaps{
		NativeAt:       true,
		NativeInterval: true,
		NativeCalendar: true,
		MinInterval:    time.Minute,
	}
}

func (b *launchdBackend) SupportsSchedule(sc Schedule) bool {
	_, err := b.triggerSpec(sc)
	return err == nil
}

func (b *launchdBackend) SupportError(sc Schedule) error {
	_, err := b.triggerSpec(sc)
	return err
}

func (b *launchdBackend) Install(sc Schedule) error {
	return b.installOrUpdate(sc, false)
}

func (b *launchdBackend) Update(sc Schedule) error {
	return b.installOrUpdate(sc, true)
}

func (b *launchdBackend) Remove(id string) error {
	home, err := b.homeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	path := launchdSchedulePlistPath(home, id)
	label := launchdScheduleLabel(id)
	if b.loaded(label) {
		// bootout may fail when called from the launchd-spawned fire process
		// (the process cannot unload its own service). This is expected for
		// one-shot schedules cleaning up after themselves. Removing the plist
		// file is the important part — launchd drops the job on next reload.
		if _, err := b.runCommand("launchctl", "bootout", launchdDomainTarget(b.uid()), path); err != nil {
			log.Printf("launchctl bootout %s (non-fatal): %v", label, err)
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (b *launchdBackend) installOrUpdate(sc Schedule, allowReplace bool) error {
	spec, err := b.triggerSpec(sc)
	if err != nil {
		return err
	}
	home, err := b.homeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	binaryPath, err := b.binaryPath()
	if err != nil {
		return err
	}
	path := launchdSchedulePlistPath(home, sc.ID)
	logPath := launchdScheduleLogPath(home)
	workingDir := launchdScheduleWorkingDirectory(home)
	content, err := launchdSchedulePlistContents(binaryPath, sc, spec, launchdScheduleOptions{
		Label:      launchdScheduleLabel(sc.ID),
		WorkingDir: workingDir,
		LogPath:    logPath,
		Env:        b.env(),
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	label := launchdScheduleLabel(sc.ID)
	if allowReplace && b.loaded(label) {
		if _, err := b.runCommand("launchctl", "bootout", launchdDomainTarget(b.uid()), path); err != nil {
			_ = os.Remove(path)
			return fmt.Errorf("launchctl bootout %s: %w", path, err)
		}
	}
	if _, err := b.runCommand("launchctl", "bootstrap", launchdDomainTarget(b.uid()), path); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("launchctl bootstrap %s: %w", path, err)
	}
	return nil
}

func (b *launchdBackend) loaded(label string) bool {
	target := launchdServiceTarget(b.uid(), label)
	_, err := b.runCommand("launchctl", "print", target)
	return err == nil
}

func (b *launchdBackend) triggerSpec(sc Schedule) (launchdTriggerSpec, error) {
	switch canonicalTrigger(sc.Spec.Trigger) {
	case TriggerAt:
		when := sc.Spec.At.In(time.Local)
		if !when.Equal(when.Truncate(time.Minute)) {
			return launchdTriggerSpec{}, fmt.Errorf("launchd one-shot schedules require minute precision")
		}
		if !launchdAtTimeRepresentable(when, b.currentTime().In(time.Local)) {
			return launchdTriggerSpec{}, fmt.Errorf("launchd one-shot schedules cannot preserve year precision for this timestamp")
		}
		return launchdTriggerSpec{
			calendar: []map[string]int{{
				"Minute": when.Minute(),
				"Hour":   when.Hour(),
				"Day":    when.Day(),
				"Month":  int(when.Month()),
			}},
		}, nil
	case TriggerInterval:
		d, err := parseEvery(sc.Spec.Interval)
		if err != nil {
			return launchdTriggerSpec{}, err
		}
		return launchdTriggerSpec{startInterval: int(d / time.Second)}, nil
	case TriggerCalendar:
		calendar, err := normalizeCalendar(sc.Spec.Calendar, sc.Spec.legacyCronSpec())
		if err != nil {
			return launchdTriggerSpec{}, err
		}
		intervals, err := launchdCalendarIntervals(calendar)
		if err != nil {
			return launchdTriggerSpec{}, err
		}
		return launchdTriggerSpec{calendar: intervals}, nil
	default:
		return launchdTriggerSpec{}, fmt.Errorf("launchd does not support trigger %q", sc.Spec.Trigger)
	}
}

func (b *launchdBackend) currentTime() time.Time {
	if b != nil && b.now != nil {
		return b.now()
	}
	return time.Now()
}

func launchdAtTimeRepresentable(when, now time.Time) bool {
	loc := when.Location()
	now = now.In(loc)
	for year := now.Year(); year <= when.Year(); year++ {
		candidate, ok := launchdAnnualOccurrence(year, when, loc)
		if !ok || candidate.Before(now) {
			continue
		}
		return candidate.Equal(when)
	}
	return false
}

func launchdAnnualOccurrence(year int, when time.Time, loc *time.Location) (time.Time, bool) {
	candidate := time.Date(year, when.Month(), when.Day(), when.Hour(), when.Minute(), 0, 0, loc)
	if candidate.Year() != year || candidate.Month() != when.Month() || candidate.Day() != when.Day() {
		return time.Time{}, false
	}
	if candidate.Hour() != when.Hour() || candidate.Minute() != when.Minute() {
		return time.Time{}, false
	}
	return candidate, true
}

func launchdCalendarIntervals(calendar *CalendarSpec) ([]map[string]int, error) {
	if calendar == nil {
		return nil, fmt.Errorf("calendar spec cannot be empty")
	}

	fields := []struct {
		key  string
		spec calendarFieldSpec
		raw  string
	}{
		{key: "Minute", spec: calendarMinuteField, raw: calendar.Minute},
		{key: "Hour", spec: calendarHourField, raw: calendar.Hour},
		{key: "Day", spec: calendarDOMField, raw: calendar.DayOfMonth},
		{key: "Month", spec: calendarMonthField, raw: calendar.Month},
		{key: "Weekday", spec: calendarDOWField, raw: calendar.DayOfWeek},
	}

	intervals := []map[string]int{{}}
	for _, field := range fields {
		values, wildcard, err := expandLaunchdCalendarField(field.raw, field.spec)
		if err != nil {
			return nil, err
		}
		if wildcard {
			continue
		}
		next := make([]map[string]int, 0, len(intervals)*len(values))
		for _, interval := range intervals {
			for _, value := range values {
				entry := make(map[string]int, len(interval)+1)
				for key, existing := range interval {
					entry[key] = existing
				}
				entry[field.key] = value
				next = append(next, entry)
				if len(next) > launchdScheduleMaxIntervals {
					return nil, fmt.Errorf("calendar schedule expands to too many launchd intervals")
				}
			}
		}
		intervals = next
	}

	sort.Slice(intervals, func(i, j int) bool {
		return launchdCalendarIntervalSortKey(intervals[i]) < launchdCalendarIntervalSortKey(intervals[j])
	})
	return intervals, nil
}

func expandLaunchdCalendarField(raw string, spec calendarFieldSpec) ([]int, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false, fmt.Errorf("%s cannot be empty", spec.name)
	}
	if raw == "*" {
		return nil, true, nil
	}

	values := map[int]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, false, fmt.Errorf("invalid %s field %q", spec.name, raw)
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			start, err := parseCalendarFieldValue(bounds[0], spec, false)
			if err != nil {
				return nil, false, err
			}
			end, err := parseCalendarFieldValue(bounds[1], spec, false)
			if err != nil {
				return nil, false, err
			}
			for value := start; value <= end; value++ {
				values[value] = struct{}{}
			}
			continue
		}
		value, err := parseCalendarFieldValue(part, spec, true)
		if err != nil {
			return nil, false, err
		}
		values[value] = struct{}{}
	}

	list := make([]int, 0, len(values))
	for value := range values {
		list = append(list, value)
	}
	sort.Ints(list)
	if len(list) == spec.max-spec.min+1 {
		full := true
		for idx, value := range list {
			if value != spec.min+idx {
				full = false
				break
			}
		}
		if full {
			return nil, true, nil
		}
	}
	return list, false, nil
}

func launchdCalendarIntervalSortKey(interval map[string]int) string {
	parts := []string{
		fmt.Sprintf("M%02d", launchdCalendarValue(interval, "Month")),
		fmt.Sprintf("D%02d", launchdCalendarValue(interval, "Day")),
		fmt.Sprintf("W%02d", launchdCalendarValue(interval, "Weekday")),
		fmt.Sprintf("H%02d", launchdCalendarValue(interval, "Hour")),
		fmt.Sprintf("m%02d", launchdCalendarValue(interval, "Minute")),
	}
	return strings.Join(parts, "")
}

func launchdCalendarValue(interval map[string]int, key string) int {
	value, ok := interval[key]
	if !ok {
		return -1
	}
	return value
}

type launchdScheduleOptions struct {
	Label      string
	WorkingDir string
	LogPath    string
	Env        map[string]string
}

func launchdSchedulePlistContents(binaryPath string, sc Schedule, spec launchdTriggerSpec, opts launchdScheduleOptions) (string, error) {
	if strings.TrimSpace(binaryPath) == "" {
		return "", fmt.Errorf("launchd binary path cannot be empty")
	}
	if strings.TrimSpace(opts.Label) == "" {
		return "", fmt.Errorf("launchd label cannot be empty")
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	b.WriteString(`  <key>Label</key>` + "\n")
	b.WriteString(`  <string>` + xmlEscape(opts.Label) + `</string>` + "\n")
	b.WriteString(`  <key>ProgramArguments</key>` + "\n")
	b.WriteString("  <array>\n")
	for _, arg := range append([]string{binaryPath}, FireCommand(sc.ID)...) {
		b.WriteString(`    <string>` + xmlEscape(arg) + `</string>` + "\n")
	}
	b.WriteString("  </array>\n")
	b.WriteString(`  <key>WorkingDirectory</key>` + "\n")
	b.WriteString(`  <string>` + xmlEscape(opts.WorkingDir) + `</string>` + "\n")
	b.WriteString(`  <key>EnvironmentVariables</key>` + "\n")
	b.WriteString("  <dict>\n")
	for _, key := range []string{"PATH", "HOME"} {
		value := strings.TrimSpace(opts.Env[key])
		if value == "" {
			continue
		}
		b.WriteString(`    <key>` + key + `</key>` + "\n")
		b.WriteString(`    <string>` + xmlEscape(value) + `</string>` + "\n")
	}
	b.WriteString("  </dict>\n")
	switch {
	case spec.startInterval > 0:
		b.WriteString(`  <key>StartInterval</key>` + "\n")
		b.WriteString(`  <integer>` + strconv.Itoa(spec.startInterval) + `</integer>` + "\n")
	case len(spec.calendar) == 1:
		b.WriteString(`  <key>StartCalendarInterval</key>` + "\n")
		writeLaunchdCalendarInterval(&b, spec.calendar[0], "  ")
	case len(spec.calendar) > 1:
		b.WriteString(`  <key>StartCalendarInterval</key>` + "\n")
		b.WriteString("  <array>\n")
		for _, interval := range spec.calendar {
			writeLaunchdCalendarInterval(&b, interval, "    ")
		}
		b.WriteString("  </array>\n")
	default:
		return "", fmt.Errorf("launchd trigger must define an interval or calendar schedule")
	}
	b.WriteString(`  <key>StandardOutPath</key>` + "\n")
	b.WriteString(`  <string>` + xmlEscape(opts.LogPath) + `</string>` + "\n")
	b.WriteString(`  <key>StandardErrorPath</key>` + "\n")
	b.WriteString(`  <string>` + xmlEscape(opts.LogPath) + `</string>` + "\n")
	b.WriteString(`</dict>` + "\n</plist>\n")
	return b.String(), nil
}

func writeLaunchdCalendarInterval(b *strings.Builder, interval map[string]int, indent string) {
	b.WriteString(indent + "<dict>\n")
	for _, key := range []string{"Minute", "Hour", "Day", "Weekday", "Month"} {
		value, ok := interval[key]
		if !ok {
			continue
		}
		b.WriteString(indent + `  <key>` + key + `</key>` + "\n")
		b.WriteString(indent + `  <integer>` + strconv.Itoa(value) + `</integer>` + "\n")
	}
	b.WriteString(indent + "</dict>\n")
}

func launchdScheduleLabel(id string) string {
	return launchdScheduleLabelPrefix + strings.TrimSpace(id)
}

func launchdSchedulePlistPath(home, id string) string {
	return filepath.Join(home, "Library", "LaunchAgents", launchdScheduleLabel(id)+".plist")
}

func launchdScheduleLogPath(home string) string {
	return filepath.Join(home, "Library", "Logs", "moxie-schedule.log")
}

func launchdScheduleWorkingDirectory(home string) string {
	if cfg, err := store.LoadConfig(); err == nil {
		if trimmed := strings.TrimSpace(cfg.DefaultCWD); trimmed != "" {
			return trimmed
		}
	}
	return filepath.Join(home, "Library", "Application Support", "Moxie", "workspace")
}

func launchdDomainTarget(uid int) string {
	return fmt.Sprintf("gui/%d", uid)
}

func launchdServiceTarget(uid int, label string) string {
	return launchdDomainTarget(uid) + "/" + label
}

func launchdScheduleEnvironment() map[string]string {
	path := os.Getenv("PATH")

	// If the current process has a minimal or empty PATH (e.g. when running
	// inside a launchd-spawned process that did not inherit the user's login
	// PATH), try to read the full PATH from the main service plist. This
	// prevents schedule plists from losing access to user-installed tools
	// like go, kubectl, cymbal, etc.
	if isMinimalPATH(path) {
		if servicePath := readServicePlistPATH(); servicePath != "" {
			path = servicePath
		} else {
			path = launchdScheduleDefaultPATH
		}
	}

	return map[string]string{
		"PATH": path,
		"HOME": os.Getenv("HOME"),
	}
}

// isMinimalPATH returns true when the PATH looks like a bare system default
// rather than a user's full login PATH.
func isMinimalPATH(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return true
	}
	return len(strings.Split(path, ":")) <= 4
}

// readServicePlistPATH reads the PATH value from the main moxie launchd
// service plist, if it exists. Returns "" on any failure.
func readServicePlistPATH() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "io.github.1broseidon.moxie.plist")
	data, err := os.ReadFile(plistPath)
	if err != nil {
		return ""
	}
	return extractPlistStringValue(string(data), "PATH")
}

// extractPlistStringValue does a simple text scan for a <key>key</key> followed
// by a <string>value</string> in a plist. It avoids pulling in a full plist
// parser for this single use case.
func extractPlistStringValue(plist, key string) string {
	needle := "<key>" + key + "</key>"
	idx := strings.Index(plist, needle)
	if idx < 0 {
		return ""
	}
	rest := plist[idx+len(needle):]
	start := strings.Index(rest, "<string>")
	if start < 0 {
		return ""
	}
	rest = rest[start+len("<string>"):]
	end := strings.Index(rest, "</string>")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		panic(fmt.Sprintf("xml escape failed: %v", err))
	}
	return b.String()
}
