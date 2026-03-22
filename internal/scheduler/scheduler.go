package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

type Trigger string

const (
	TriggerAt       Trigger = "at"
	TriggerInterval Trigger = "interval"
	TriggerCalendar Trigger = "calendar"

	// TriggerCron is a legacy alias accepted on input and during on-disk migration.
	TriggerCron Trigger = "cron"
)

type Action string

const (
	ActionSend     Action = "send"
	ActionDispatch Action = "dispatch"
)

type CalendarSpec struct {
	Minute     string `json:"minute,omitempty"`
	Hour       string `json:"hour,omitempty"`
	DayOfMonth string `json:"day_of_month,omitempty"`
	Month      string `json:"month,omitempty"`
	DayOfWeek  string `json:"day_of_week,omitempty"`
	Cron       string `json:"cron,omitempty"`
}

func (c *CalendarSpec) CronSpec() string {
	if c == nil {
		return ""
	}
	parts := []string{
		strings.TrimSpace(c.Minute),
		strings.TrimSpace(c.Hour),
		strings.TrimSpace(c.DayOfMonth),
		strings.TrimSpace(c.Month),
		strings.TrimSpace(c.DayOfWeek),
	}
	return strings.Join(parts, " ")
}

func (c *CalendarSpec) DisplaySpec() string {
	if c == nil {
		return ""
	}
	if raw := strings.TrimSpace(c.Cron); raw != "" {
		return raw
	}
	return c.CronSpec()
}

type Schedule struct {
	ID             string        `json:"id"`
	Trigger        Trigger       `json:"trigger"`
	Action         Action        `json:"action"`
	At             time.Time     `json:"at,omitempty"`
	Interval       string        `json:"interval,omitempty"`
	Calendar       *CalendarSpec `json:"calendar,omitempty"`
	Cron           string        `json:"cron,omitempty"` // legacy on-disk field
	Text           string        `json:"text"`
	ConversationID string        `json:"conversation_id,omitempty"`
	Backend        string        `json:"backend,omitempty"`
	Model          string        `json:"model,omitempty"`
	ThreadID       string        `json:"thread_id,omitempty"`
	CWD            string        `json:"cwd,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	NextRun        time.Time     `json:"next_run"`
	LastRun        time.Time     `json:"last_run,omitempty"`
	RunningJobID   string        `json:"running_job_id,omitempty"`
	ManagedBy      string        `json:"managed_by,omitempty"`
	SyncState      string        `json:"sync_state,omitempty"`
	SyncError      string        `json:"sync_error,omitempty"`
}

type AddInput struct {
	Trigger        Trigger
	Action         Action
	At             string
	In             string
	Every          string
	Cron           string
	Calendar       *CalendarSpec
	Text           string
	ConversationID string
	Backend        string
	Model          string
	ThreadID       string
	CWD            string
	Now            time.Time
}

type Store struct {
	path string
	loc  *time.Location
	mu   sync.Mutex
}

type fileData struct {
	Schedules []Schedule `json:"schedules"`
}

func NewStore(path string, loc *time.Location) *Store {
	if loc == nil {
		loc = time.Local
	}
	return &Store{path: path, loc: loc}
}

func (s *Store) Add(input AddInput) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	schedules, err := s.load()
	if err != nil {
		return Schedule{}, err
	}

	now := input.Now
	if now.IsZero() {
		now = time.Now().In(s.loc)
	}

	sc, err := s.buildSchedule(input, now)
	if err != nil {
		return Schedule{}, err
	}

	schedules = append(schedules, sc)
	sortSchedules(schedules)
	if err := s.save(schedules); err != nil {
		return Schedule{}, err
	}
	return sc, nil
}

func (s *Store) List() ([]Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	schedules, err := s.load()
	if err != nil {
		return nil, err
	}
	sortSchedules(schedules)
	return schedules, nil
}

func (s *Store) Get(id string) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	schedules, err := s.load()
	if err != nil {
		return Schedule{}, err
	}
	for _, sc := range schedules {
		if sc.ID == id {
			return sc, nil
		}
	}
	return Schedule{}, os.ErrNotExist
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	schedules, err := s.load()
	if err != nil {
		return err
	}
	next := schedules[:0]
	found := false
	for _, sc := range schedules {
		if sc.ID == id {
			if sc.RunningJobID != "" {
				return fmt.Errorf("schedule %s is running via job %s", id, sc.RunningJobID)
			}
			found = true
			continue
		}
		next = append(next, sc)
	}
	if !found {
		return os.ErrNotExist
	}
	return s.save(next)
}

func (s *Store) Due(now time.Time) ([]Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	schedules, err := s.load()
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().In(s.loc)
	}
	due := make([]Schedule, 0)
	for _, sc := range schedules {
		if sc.RunningJobID != "" {
			continue
		}
		if sc.NextRun.IsZero() || sc.NextRun.After(now) {
			continue
		}
		due = append(due, sc)
	}
	sortSchedules(due)
	return due, nil
}

func (s *Store) AttachJob(id, jobID string) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(jobID) == "" {
		return Schedule{}, fmt.Errorf("job id cannot be empty")
	}
	schedules, err := s.load()
	if err != nil {
		return Schedule{}, err
	}
	for i, sc := range schedules {
		if sc.ID != id {
			continue
		}
		if sc.RunningJobID != "" && sc.RunningJobID != jobID {
			return Schedule{}, fmt.Errorf("schedule %s already attached to job %s", id, sc.RunningJobID)
		}
		sc.RunningJobID = jobID
		schedules[i] = sc
		sortSchedules(schedules)
		if err := s.save(schedules); err != nil {
			return Schedule{}, err
		}
		return sc, nil
	}
	return Schedule{}, os.ErrNotExist
}

func (s *Store) MarkDone(id, jobID string, finishedAt time.Time) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	schedules, err := s.load()
	if err != nil {
		return Schedule{}, err
	}
	if finishedAt.IsZero() {
		finishedAt = time.Now().In(s.loc)
	}
	for i, sc := range schedules {
		if sc.ID != id {
			continue
		}
		if sc.RunningJobID != "" && sc.RunningJobID != jobID {
			return Schedule{}, fmt.Errorf("schedule %s attached to different job %s", id, sc.RunningJobID)
		}
		sc.RunningJobID = ""
		sc.LastRun = finishedAt
		if sc.Trigger == TriggerAt {
			next := append(schedules[:i:i], schedules[i+1:]...)
			if err := s.save(next); err != nil {
				return Schedule{}, err
			}
			return sc, nil
		}
		nextRun, err := nextScheduleRun(sc, finishedAt, s.loc)
		if err != nil {
			return Schedule{}, err
		}
		sc.NextRun = nextRun
		schedules[i] = sc
		sortSchedules(schedules)
		if err := s.save(schedules); err != nil {
			return Schedule{}, err
		}
		return sc, nil
	}
	return Schedule{}, os.ErrNotExist
}

func (s *Store) Repair(jobExists func(string) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	schedules, err := s.load()
	if err != nil {
		return err
	}
	changed := false
	for i, sc := range schedules {
		if sc.RunningJobID == "" {
			continue
		}
		if jobExists(sc.RunningJobID) {
			continue
		}
		sc.RunningJobID = ""
		schedules[i] = sc
		changed = true
	}
	if !changed {
		return nil
	}
	sortSchedules(schedules)
	return s.save(schedules)
}

func (s *Store) buildSchedule(input AddInput, now time.Time) (Schedule, error) {
	text := compactText(input.Text)
	if text == "" {
		return Schedule{}, fmt.Errorf("text cannot be empty")
	}

	action := input.Action
	if action != ActionSend && action != ActionDispatch {
		return Schedule{}, fmt.Errorf("action must be send or dispatch")
	}

	trigger := canonicalTrigger(input.Trigger)
	if !supportedTrigger(input.Trigger) {
		return Schedule{}, fmt.Errorf("trigger must be at, interval, or calendar")
	}

	sc := Schedule{
		ID:             newID(now),
		Trigger:        trigger,
		Action:         action,
		Text:           text,
		ConversationID: strings.TrimSpace(input.ConversationID),
		Backend:        strings.TrimSpace(input.Backend),
		Model:          strings.TrimSpace(input.Model),
		ThreadID:       strings.TrimSpace(input.ThreadID),
		CWD:            strings.TrimSpace(input.CWD),
		CreatedAt:      now,
		ManagedBy:      ManagedByInProcess,
		SyncState:      SyncStateFallback,
	}

	switch trigger {
	case TriggerAt:
		at, err := resolveAt(strings.TrimSpace(input.At), strings.TrimSpace(input.In), now, s.loc)
		if err != nil {
			return Schedule{}, err
		}
		if !at.After(now) {
			return Schedule{}, fmt.Errorf("scheduled time must be in the future")
		}
		sc.At = at
		sc.NextRun = at
	case TriggerInterval:
		d, err := parseEvery(strings.TrimSpace(input.Every))
		if err != nil {
			return Schedule{}, err
		}
		sc.Interval = d.String()
		sc.NextRun = now.Add(d)
	case TriggerCalendar:
		calendar, nextRun, err := resolveCalendar(strings.TrimSpace(input.Cron), input.Calendar, now, s.loc)
		if err != nil {
			return Schedule{}, err
		}
		sc.Calendar = calendar
		sc.NextRun = nextRun
	default:
		return Schedule{}, fmt.Errorf("unsupported trigger: %s", trigger)
	}

	return sc, nil
}

func (s *Store) load() ([]Schedule, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc fileData
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	normalized, err := normalizeSchedules(doc.Schedules, s.loc)
	if err != nil {
		return nil, err
	}
	sortSchedules(normalized)
	return normalized, nil
}

func (s *Store) save(schedules []Schedule) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	normalized, err := normalizeSchedules(schedules, s.loc)
	if err != nil {
		return err
	}
	schedules = normalized
	sortSchedules(schedules)
	data, err := json.MarshalIndent(fileData{Schedules: schedules}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

func normalizeSchedules(schedules []Schedule, loc *time.Location) ([]Schedule, error) {
	normalized := make([]Schedule, 0, len(schedules))
	for _, sc := range schedules {
		upgraded, err := normalizeLoadedSchedule(sc, loc)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, upgraded)
	}
	return normalized, nil
}

func normalizeLoadedSchedule(sc Schedule, loc *time.Location) (Schedule, error) {
	if loc == nil {
		loc = time.Local
	}
	sc.Trigger = inferTrigger(sc)
	if sc.ManagedBy == "" {
		sc.ManagedBy = ManagedByInProcess
	}
	if sc.SyncState == "" {
		sc.SyncState = SyncStateFallback
	}

	switch sc.Trigger {
	case TriggerAt:
		if sc.NextRun.IsZero() && !sc.At.IsZero() {
			sc.NextRun = sc.At
		}
	case TriggerInterval:
		d, err := parseEvery(sc.Interval)
		if err != nil {
			return Schedule{}, err
		}
		sc.Interval = d.String()
		if sc.NextRun.IsZero() {
			base := firstNonZeroTime(sc.LastRun, sc.CreatedAt, time.Now().In(loc))
			sc.NextRun = base.Add(d)
		}
	case TriggerCalendar:
		calendar, err := normalizeCalendar(sc.Calendar, sc.Cron)
		if err != nil {
			return Schedule{}, err
		}
		sc.Calendar = calendar
		sc.Cron = ""
		if sc.NextRun.IsZero() {
			base := firstNonZeroTime(sc.LastRun, sc.CreatedAt, time.Now().In(loc))
			nextRun, err := nextCalendarRun(sc.Calendar, base, loc)
			if err != nil {
				return Schedule{}, err
			}
			sc.NextRun = nextRun
		}
	default:
		return Schedule{}, fmt.Errorf("unsupported trigger: %s", sc.Trigger)
	}

	return sc, nil
}

func inferTrigger(sc Schedule) Trigger {
	switch canonicalTrigger(sc.Trigger) {
	case TriggerAt, TriggerInterval, TriggerCalendar:
		return canonicalTrigger(sc.Trigger)
	}
	switch {
	case !sc.At.IsZero():
		return TriggerAt
	case strings.TrimSpace(sc.Interval) != "":
		return TriggerInterval
	case sc.Calendar != nil || strings.TrimSpace(sc.Cron) != "":
		return TriggerCalendar
	default:
		return canonicalTrigger(sc.Trigger)
	}
}

func sortSchedules(schedules []Schedule) {
	sort.Slice(schedules, func(i, j int) bool {
		if schedules[i].NextRun.Equal(schedules[j].NextRun) {
			return schedules[i].CreatedAt.Before(schedules[j].CreatedAt)
		}
		return schedules[i].NextRun.Before(schedules[j].NextRun)
	})
}

func supportedTrigger(trigger Trigger) bool {
	switch trigger {
	case TriggerAt, TriggerInterval, TriggerCalendar, TriggerCron:
		return true
	default:
		return false
	}
}

func canonicalTrigger(trigger Trigger) Trigger {
	if trigger == TriggerCron {
		return TriggerCalendar
	}
	return trigger
}

func newID(now time.Time) string {
	return fmt.Sprintf("sch-%d", now.UnixNano())
}

func parseAt(raw string, loc *time.Location) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("at time cannot be empty")
	}

	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04",
		"2006-01-02 15:04",
	}
	for _, format := range formats {
		var (
			at  time.Time
			err error
		)
		if format == time.RFC3339 {
			at, err = time.Parse(format, raw)
		} else {
			at, err = time.ParseInLocation(format, raw, loc)
		}
		if err == nil {
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid at time %q", raw)
}

func resolveAt(atRaw, inRaw string, now time.Time, loc *time.Location) (time.Time, error) {
	switch {
	case atRaw != "" && inRaw != "":
		return time.Time{}, fmt.Errorf("use either at or in, not both")
	case inRaw != "":
		d, err := parseIn(inRaw)
		if err != nil {
			return time.Time{}, err
		}
		return now.Add(d), nil
	case atRaw != "":
		return parseAt(atRaw, loc)
	default:
		return time.Time{}, fmt.Errorf("at time cannot be empty")
	}
}

func parseIn(raw string) (time.Duration, error) {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), " ", "")
	if raw == "" {
		return 0, fmt.Errorf("in duration cannot be empty")
	}

	var total time.Duration
	for i := 0; i < len(raw); {
		j := i
		for j < len(raw) && raw[j] >= '0' && raw[j] <= '9' {
			j++
		}
		if j == i || j >= len(raw) {
			return 0, fmt.Errorf("invalid in duration %q", raw)
		}

		n, err := time.ParseDuration(raw[i:j] + string(raw[j]))
		if err == nil && raw[j] != 'd' {
			total += n
			i = j + 1
			continue
		}

		value, convErr := time.ParseDuration(raw[i:j] + "h")
		if convErr != nil {
			return 0, fmt.Errorf("invalid in duration %q", raw)
		}
		switch raw[j] {
		case 'h':
			total += value
		case 'm':
			total += value / time.Hour * time.Minute
		case 'd':
			total += value * 24
		default:
			return 0, fmt.Errorf("unsupported in duration unit %q", string(raw[j]))
		}
		i = j + 1
	}

	if total <= 0 {
		return 0, fmt.Errorf("in duration must be greater than zero")
	}
	return total, nil
}

func parseEvery(raw string) (time.Duration, error) {
	d, err := parseIn(raw)
	if err != nil {
		return 0, err
	}
	if d < time.Minute {
		return 0, fmt.Errorf("interval must be at least 1m")
	}
	return d, nil
}

func resolveCalendar(rawCron string, calendar *CalendarSpec, now time.Time, loc *time.Location) (*CalendarSpec, time.Time, error) {
	switch {
	case rawCron != "" && calendar != nil:
		return nil, time.Time{}, fmt.Errorf("use either cron or calendar, not both")
	case rawCron != "":
		parsed, err := parseCronCalendar(rawCron)
		if err != nil {
			return nil, time.Time{}, err
		}
		nextRun, err := nextCalendarRun(parsed, now, loc)
		if err != nil {
			return nil, time.Time{}, err
		}
		return parsed, nextRun, nil
	case calendar != nil:
		normalized, err := normalizeCalendar(calendar, "")
		if err != nil {
			return nil, time.Time{}, err
		}
		nextRun, err := nextCalendarRun(normalized, now, loc)
		if err != nil {
			return nil, time.Time{}, err
		}
		return normalized, nextRun, nil
	default:
		return nil, time.Time{}, fmt.Errorf("calendar spec cannot be empty")
	}
}

func parseCronCalendar(raw string) (*CalendarSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("cron spec cannot be empty")
	}
	expanded, err := expandCronDescriptor(raw)
	if err != nil {
		return nil, err
	}
	parts := strings.Fields(expanded)
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron spec must have 5 fields")
	}
	calendar := &CalendarSpec{
		Minute:     parts[0],
		Hour:       parts[1],
		DayOfMonth: parts[2],
		Month:      parts[3],
		DayOfWeek:  parts[4],
		Cron:       raw,
	}
	if err := validateCalendar(calendar); err != nil {
		return nil, err
	}
	return calendar, nil
}

func normalizeCalendar(calendar *CalendarSpec, legacyCron string) (*CalendarSpec, error) {
	if calendar == nil {
		if strings.TrimSpace(legacyCron) == "" {
			return nil, fmt.Errorf("calendar spec cannot be empty")
		}
		return parseCronCalendar(legacyCron)
	}

	normalized := &CalendarSpec{
		Minute:     strings.TrimSpace(calendar.Minute),
		Hour:       strings.TrimSpace(calendar.Hour),
		DayOfMonth: strings.TrimSpace(calendar.DayOfMonth),
		Month:      strings.TrimSpace(calendar.Month),
		DayOfWeek:  strings.TrimSpace(calendar.DayOfWeek),
		Cron:       strings.TrimSpace(calendar.Cron),
	}
	if normalized.Cron == "" && strings.TrimSpace(legacyCron) != "" {
		normalized.Cron = strings.TrimSpace(legacyCron)
	}
	if normalized.Minute == "" || normalized.Hour == "" || normalized.DayOfMonth == "" || normalized.Month == "" || normalized.DayOfWeek == "" {
		if normalized.Cron == "" {
			return nil, fmt.Errorf("calendar spec requires minute, hour, day_of_month, month, and day_of_week")
		}
		return parseCronCalendar(normalized.Cron)
	}
	if err := validateCalendar(normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func validateCalendar(calendar *CalendarSpec) error {
	if calendar == nil {
		return fmt.Errorf("calendar spec cannot be empty")
	}
	spec := calendar.CronSpec()
	if strings.TrimSpace(spec) == "" {
		return fmt.Errorf("cron spec cannot be empty")
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	_, err := parser.Parse(spec)
	return err
}

func expandCronDescriptor(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "@yearly", "@annually":
		return "0 0 1 1 *", nil
	case "@monthly":
		return "0 0 1 * *", nil
	case "@weekly":
		return "0 0 * * 0", nil
	case "@daily", "@midnight":
		return "0 0 * * *", nil
	case "@hourly":
		return "0 * * * *", nil
	case "@reboot":
		return "", fmt.Errorf("cron descriptor @reboot is not supported")
	default:
		return raw, nil
	}
}

func nextScheduleRun(sc Schedule, after time.Time, loc *time.Location) (time.Time, error) {
	switch sc.Trigger {
	case TriggerInterval:
		return nextIntervalRun(sc.Interval, after)
	case TriggerCalendar:
		return nextCalendarRun(sc.Calendar, after, loc)
	case TriggerCron:
		return nextCalendarRun(sc.Calendar, after, loc)
	default:
		return time.Time{}, fmt.Errorf("trigger %s does not support next run calculation", sc.Trigger)
	}
}

func nextIntervalRun(raw string, after time.Time) (time.Time, error) {
	d, err := parseEvery(raw)
	if err != nil {
		return time.Time{}, err
	}
	return after.Add(d), nil
}

func nextCalendarRun(calendar *CalendarSpec, after time.Time, loc *time.Location) (time.Time, error) {
	if calendar == nil {
		return time.Time{}, fmt.Errorf("calendar spec cannot be empty")
	}
	spec := calendar.CronSpec()
	if strings.TrimSpace(spec) == "" {
		return time.Time{}, fmt.Errorf("cron spec cannot be empty")
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	sched, err := parser.Parse(spec)
	if err != nil {
		return time.Time{}, err
	}
	if loc == nil {
		loc = time.Local
	}
	next := sched.Next(after.In(loc))
	if next.IsZero() {
		return time.Time{}, errors.New("calendar schedule has no next run")
	}
	return next, nil
}

func nextCronRun(spec string, after time.Time, loc *time.Location) (time.Time, error) {
	calendar, err := parseCronCalendar(spec)
	if err != nil {
		return time.Time{}, err
	}
	return nextCalendarRun(calendar, after, loc)
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func compactText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}
