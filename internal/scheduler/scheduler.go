package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	jobstore "github.com/1broseidon/moxie/internal/store"
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

type ScheduleSpec struct {
	Trigger  Trigger       `json:"trigger"`
	At       time.Time     `json:"at,omitempty"`
	Interval string        `json:"interval,omitempty"`
	Calendar *CalendarSpec `json:"calendar,omitempty"`

	legacyCron string `json:"-"`
}

func (s ScheduleSpec) legacyCronSpec() string {
	return strings.TrimSpace(s.legacyCron)
}

type ScheduleSync struct {
	ManagedBy string `json:"managed_by,omitempty"`
	State     string `json:"state,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (s ScheduleSync) isZero() bool {
	return strings.TrimSpace(s.ManagedBy) == "" && strings.TrimSpace(s.State) == "" && strings.TrimSpace(s.Error) == ""
}

type Schedule struct {
	ID             string       `json:"id"`
	Action         Action       `json:"action"`
	Spec           ScheduleSpec `json:"spec"`
	Text           string       `json:"text"`
	ConversationID string       `json:"conversation_id,omitempty"`
	Backend        string       `json:"backend,omitempty"`
	Model          string       `json:"model,omitempty"`
	ThreadID       string       `json:"thread_id,omitempty"`
	CWD            string       `json:"cwd,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	NextRun        time.Time    `json:"next_run"`
	LastRun        time.Time    `json:"last_run,omitempty"`
	RunningJobID   string       `json:"running_job_id,omitempty"`
	Sync           ScheduleSync `json:"sync,omitempty"`
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
	path     string
	loc      *time.Location
	mu       sync.Mutex
	backends *backendReconciler
}

type fileData struct {
	Schedules []Schedule `json:"schedules"`
}

type scheduleJSON struct {
	ID             string        `json:"id"`
	Action         Action        `json:"action"`
	Spec           *ScheduleSpec `json:"spec,omitempty"`
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
	Sync           *ScheduleSync `json:"sync,omitempty"`
}

type scheduleLegacyJSON struct {
	ID             string        `json:"id"`
	Action         Action        `json:"action"`
	Spec           *ScheduleSpec `json:"spec,omitempty"`
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
	Sync           *ScheduleSync `json:"sync,omitempty"`

	Trigger   Trigger       `json:"trigger,omitempty"`
	At        time.Time     `json:"at,omitempty"`
	Interval  string        `json:"interval,omitempty"`
	Calendar  *CalendarSpec `json:"calendar,omitempty"`
	Cron      string        `json:"cron,omitempty"`
	ManagedBy string        `json:"managed_by,omitempty"`
	SyncState string        `json:"sync_state,omitempty"`
	SyncError string        `json:"sync_error,omitempty"`
}

func (sc Schedule) MarshalJSON() ([]byte, error) {
	raw := scheduleJSON{
		ID:             sc.ID,
		Action:         sc.Action,
		Spec:           &sc.Spec,
		Text:           sc.Text,
		ConversationID: sc.ConversationID,
		Backend:        sc.Backend,
		Model:          sc.Model,
		ThreadID:       sc.ThreadID,
		CWD:            sc.CWD,
		CreatedAt:      sc.CreatedAt,
		NextRun:        sc.NextRun,
		LastRun:        sc.LastRun,
		RunningJobID:   sc.RunningJobID,
	}
	if !sc.Sync.isZero() {
		sync := sc.Sync
		raw.Sync = &sync
	}
	return json.Marshal(raw)
}

func (sc *Schedule) UnmarshalJSON(data []byte) error {
	var raw scheduleLegacyJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*sc = Schedule{
		ID:             raw.ID,
		Action:         raw.Action,
		Text:           raw.Text,
		ConversationID: raw.ConversationID,
		Backend:        raw.Backend,
		Model:          raw.Model,
		ThreadID:       raw.ThreadID,
		CWD:            raw.CWD,
		CreatedAt:      raw.CreatedAt,
		NextRun:        raw.NextRun,
		LastRun:        raw.LastRun,
		RunningJobID:   raw.RunningJobID,
	}

	if raw.Spec != nil {
		sc.Spec = *raw.Spec
	}
	if sc.Spec.Trigger == "" {
		sc.Spec.Trigger = raw.Trigger
	}
	if sc.Spec.At.IsZero() {
		sc.Spec.At = raw.At
	}
	if strings.TrimSpace(sc.Spec.Interval) == "" {
		sc.Spec.Interval = raw.Interval
	}
	if sc.Spec.Calendar == nil {
		sc.Spec.Calendar = raw.Calendar
	}
	sc.Spec.legacyCron = strings.TrimSpace(raw.Cron)

	if raw.Sync != nil {
		sc.Sync = *raw.Sync
	}
	if sc.Sync.ManagedBy == "" {
		sc.Sync.ManagedBy = strings.TrimSpace(raw.ManagedBy)
	}
	if sc.Sync.State == "" {
		sc.Sync.State = strings.TrimSpace(raw.SyncState)
	}
	if sc.Sync.Error == "" {
		sc.Sync.Error = strings.TrimSpace(raw.SyncError)
	}

	return nil
}

func NewStore(path string, loc *time.Location) *Store {
	return newStoreWithBackends(path, loc, defaultBackendReconciler())
}

func newStoreWithBackends(path string, loc *time.Location, backends *backendReconciler) *Store {
	if loc == nil {
		loc = time.Local
	}
	if backends == nil {
		backends = defaultBackendReconciler()
	}
	return &Store{path: path, loc: loc, backends: backends}
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
	sc, err = s.backends.Materialize(sc)
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
	var removed Schedule
	for _, sc := range schedules {
		if sc.ID == id {
			if sc.RunningJobID != "" && jobstore.JobExists(sc.RunningJobID) {
				return fmt.Errorf("schedule %s is running via job %s", id, sc.RunningJobID)
			}
			found = true
			removed = sc
			removed.RunningJobID = ""
			continue
		}
		next = append(next, sc)
	}
	if !found {
		return os.ErrNotExist
	}
	if err := s.backends.Remove(removed); err != nil {
		return err
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
		if managedBy := strings.TrimSpace(sc.Sync.ManagedBy); managedBy != "" && managedBy != s.backends.fallback.Name() {
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
		if sc.Spec.Trigger == TriggerAt {
			next := append(schedules[:i:i], schedules[i+1:]...)
			if err := s.backends.Remove(sc); err != nil {
				return Schedule{}, err
			}
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
		ID:     newID(now),
		Action: action,
		Spec: ScheduleSpec{
			Trigger: trigger,
		},
		Text:           text,
		ConversationID: strings.TrimSpace(input.ConversationID),
		Backend:        strings.TrimSpace(input.Backend),
		Model:          strings.TrimSpace(input.Model),
		ThreadID:       strings.TrimSpace(input.ThreadID),
		CWD:            strings.TrimSpace(input.CWD),
		CreatedAt:      now,
	}

	switch trigger {
	case TriggerAt:
		at, err := resolveAt(strings.TrimSpace(input.At), strings.TrimSpace(input.In), now, s.loc)
		if err != nil {
			return Schedule{}, err
		}
		if strings.TrimSpace(input.In) != "" {
			at = roundUpToMinute(at)
		}
		if !at.After(now) {
			return Schedule{}, fmt.Errorf("scheduled time must be in the future")
		}
		sc.Spec.At = at
		sc.NextRun = at
	case TriggerInterval:
		d, err := parseEvery(strings.TrimSpace(input.Every))
		if err != nil {
			return Schedule{}, err
		}
		sc.Spec.Interval = d.String()
		sc.NextRun = now.Add(d)
	case TriggerCalendar:
		calendar, nextRun, err := resolveCalendar(strings.TrimSpace(input.Cron), input.Calendar, now, s.loc)
		if err != nil {
			return Schedule{}, err
		}
		sc.Spec.Calendar = calendar
		sc.NextRun = nextRun
	default:
		return Schedule{}, fmt.Errorf("unsupported trigger: %s", trigger)
	}

	return sc, nil
}

func (s *Store) load() ([]Schedule, error) {
	doc, err := s.loadFileData()
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeSchedules(doc.Schedules, s.loc, s.backends)
	if err != nil {
		return nil, err
	}
	sortSchedules(normalized)
	return normalized, nil
}

func (s *Store) save(schedules []Schedule) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	normalized, err := normalizeSchedules(schedules, s.loc, s.backends)
	if err != nil {
		return err
	}
	schedules = normalized
	sortSchedules(schedules)
	data, err := json.MarshalIndent(fileData{Schedules: schedules}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.backupPath(), data, 0o600); err != nil {
		return err
	}
	return writeFileAtomic(s.path, data, 0o600)
}

func (s *Store) backupPath() string {
	return s.path + ".bak"
}

func (s *Store) loadFileData() (fileData, error) {
	doc, err := readScheduleFileData(s.path)
	if err == nil {
		return doc, nil
	}
	backupPath := s.backupPath()
	backup, backupErr := readScheduleFileData(backupPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if errors.Is(backupErr, os.ErrNotExist) {
			return fileData{}, nil
		}
		if backupErr != nil {
			return fileData{}, backupErr
		}
		log.Printf("warning: schedule store %s missing; using backup %s", s.path, backupPath)
		return backup, nil
	default:
		log.Printf("warning: failed to load schedule store %s: %v", s.path, err)
		if backupErr != nil {
			return fileData{}, fmt.Errorf("load schedule store %s: %w (backup %s: %v)", s.path, err, backupPath, backupErr)
		}
		log.Printf("warning: using schedule store backup %s", backupPath)
		return backup, nil
	}
}

func readScheduleFileData(path string) (fileData, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fileData{}, err
	}
	var doc fileData
	if err := json.Unmarshal(data, &doc); err != nil {
		return fileData{}, err
	}
	return doc, nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = replaceFile(tmpPath, path); err != nil {
		return err
	}
	return nil
}

func replaceFile(src, dst string) error {
	renameErr := os.Rename(src, dst)
	if renameErr == nil {
		return nil
	}
	if runtime.GOOS != "windows" {
		return renameErr
	}
	if removeErr := os.Remove(dst); removeErr != nil && !os.IsNotExist(removeErr) {
		return renameErr
	}
	return os.Rename(src, dst)
}

func normalizeSchedules(schedules []Schedule, loc *time.Location, backends *backendReconciler) ([]Schedule, error) {
	normalized := make([]Schedule, 0, len(schedules))
	for _, sc := range schedules {
		upgraded, err := normalizeLoadedSchedule(sc, loc, backends)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, upgraded)
	}
	return normalized, nil
}

func normalizeLoadedSchedule(sc Schedule, loc *time.Location, backends *backendReconciler) (Schedule, error) {
	if loc == nil {
		loc = time.Local
	}
	if backends == nil {
		backends = defaultBackendReconciler()
	}
	sc.Spec.Trigger = inferTrigger(sc)

	switch sc.Spec.Trigger {
	case TriggerAt:
		if sc.NextRun.IsZero() && !sc.Spec.At.IsZero() {
			sc.NextRun = sc.Spec.At
		}
	case TriggerInterval:
		d, err := parseEvery(sc.Spec.Interval)
		if err != nil {
			return Schedule{}, err
		}
		sc.Spec.Interval = d.String()
		if sc.NextRun.IsZero() {
			base := firstNonZeroTime(sc.LastRun, sc.CreatedAt, time.Now().In(loc))
			sc.NextRun = base.Add(d)
		}
	case TriggerCalendar:
		calendar, err := normalizeCalendar(sc.Spec.Calendar, sc.Spec.legacyCronSpec())
		if err != nil {
			return Schedule{}, err
		}
		sc.Spec.Calendar = calendar
		sc.Spec.legacyCron = ""
		if sc.NextRun.IsZero() {
			base := firstNonZeroTime(sc.LastRun, sc.CreatedAt, time.Now().In(loc))
			nextRun, err := nextCalendarRun(sc.Spec.Calendar, base, loc)
			if err != nil {
				return Schedule{}, err
			}
			sc.NextRun = nextRun
		}
	default:
		return Schedule{}, fmt.Errorf("unsupported trigger: %s", sc.Spec.Trigger)
	}

	return backends.Normalize(sc)
}

func inferTrigger(sc Schedule) Trigger {
	switch canonicalTrigger(sc.Spec.Trigger) {
	case TriggerAt, TriggerInterval, TriggerCalendar:
		return canonicalTrigger(sc.Spec.Trigger)
	}
	switch {
	case !sc.Spec.At.IsZero():
		return TriggerAt
	case strings.TrimSpace(sc.Spec.Interval) != "":
		return TriggerInterval
	case sc.Spec.Calendar != nil || sc.Spec.legacyCronSpec() != "":
		return TriggerCalendar
	default:
		return canonicalTrigger(sc.Spec.Trigger)
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

func roundUpToMinute(at time.Time) time.Time {
	truncated := at.Truncate(time.Minute)
	if at.Equal(truncated) {
		return at
	}
	return truncated.Add(time.Minute)
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

type calendarFieldSpec struct {
	name             string
	min              int
	max              int
	aliases          map[string]int
	allowSundaySeven bool
}

var (
	calendarMinuteField = calendarFieldSpec{name: "minute", min: 0, max: 59}
	calendarHourField   = calendarFieldSpec{name: "hour", min: 0, max: 23}
	calendarDOMField    = calendarFieldSpec{name: "day_of_month", min: 1, max: 31}
	calendarMonthField  = calendarFieldSpec{
		name: "month",
		min:  1,
		max:  12,
		aliases: map[string]int{
			"JAN": 1,
			"FEB": 2,
			"MAR": 3,
			"APR": 4,
			"MAY": 5,
			"JUN": 6,
			"JUL": 7,
			"AUG": 8,
			"SEP": 9,
			"OCT": 10,
			"NOV": 11,
			"DEC": 12,
		},
	}
	calendarDOWField = calendarFieldSpec{
		name:             "day_of_week",
		min:              0,
		max:              6,
		allowSundaySeven: true,
		aliases: map[string]int{
			"SUN": 0,
			"MON": 1,
			"TUE": 2,
			"WED": 3,
			"THU": 4,
			"FRI": 5,
			"SAT": 6,
		},
	}
)

func normalizeCalendarFields(minute, hour, dayOfMonth, month, dayOfWeek string) (*CalendarSpec, error) {
	normalizedMinute, err := normalizeCalendarField(minute, calendarMinuteField)
	if err != nil {
		return nil, err
	}
	normalizedHour, err := normalizeCalendarField(hour, calendarHourField)
	if err != nil {
		return nil, err
	}
	normalizedDayOfMonth, err := normalizeCalendarField(dayOfMonth, calendarDOMField)
	if err != nil {
		return nil, err
	}
	normalizedMonth, err := normalizeCalendarField(month, calendarMonthField)
	if err != nil {
		return nil, err
	}
	normalizedDayOfWeek, err := normalizeCalendarField(dayOfWeek, calendarDOWField)
	if err != nil {
		return nil, err
	}
	if normalizedDayOfMonth != "*" && normalizedDayOfWeek != "*" {
		return nil, fmt.Errorf("cron spec cannot restrict both day_of_month and day_of_week in portable calendar mode")
	}
	calendar := &CalendarSpec{
		Minute:     normalizedMinute,
		Hour:       normalizedHour,
		DayOfMonth: normalizedDayOfMonth,
		Month:      normalizedMonth,
		DayOfWeek:  normalizedDayOfWeek,
	}
	if err := validateCalendar(calendar); err != nil {
		return nil, err
	}
	return calendar, nil
}

func normalizeCalendarField(raw string, spec calendarFieldSpec) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%s cannot be empty", spec.name)
	}
	parts := strings.Split(raw, ",")
	normalizedParts := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return "", fmt.Errorf("invalid %s field %q", spec.name, raw)
		}
		if strings.ContainsAny(part, " \t\n\r") {
			return "", fmt.Errorf("invalid %s field %q", spec.name, raw)
		}
		if part == "*" {
			if len(parts) > 1 {
				return "", fmt.Errorf("invalid %s field %q", spec.name, raw)
			}
			normalizedParts = append(normalizedParts, part)
			continue
		}
		if strings.Contains(part, "/") {
			return "", fmt.Errorf("%s step expressions are not supported in portable cron", spec.name)
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			start, err := parseCalendarFieldValue(bounds[0], spec, false)
			if err != nil {
				return "", err
			}
			end, err := parseCalendarFieldValue(bounds[1], spec, false)
			if err != nil {
				return "", err
			}
			if start > end {
				return "", fmt.Errorf("invalid %s range %q", spec.name, part)
			}
			normalizedParts = append(normalizedParts, fmt.Sprintf("%d-%d", start, end))
			continue
		}
		value, err := parseCalendarFieldValue(part, spec, true)
		if err != nil {
			return "", err
		}
		normalizedParts = append(normalizedParts, strconv.Itoa(value))
	}
	return strings.Join(normalizedParts, ","), nil
}

func parseCalendarFieldValue(raw string, spec calendarFieldSpec, allowSundaySeven bool) (int, error) {
	token := strings.ToUpper(strings.TrimSpace(raw))
	if token == "" {
		return 0, fmt.Errorf("invalid %s field %q", spec.name, raw)
	}
	if value, ok := spec.aliases[token]; ok {
		return value, nil
	}
	value, err := strconv.Atoi(token)
	if err != nil {
		return 0, fmt.Errorf("unsupported %s value %q in portable cron", spec.name, raw)
	}
	if spec.allowSundaySeven && value == 7 {
		if !allowSundaySeven {
			return 0, fmt.Errorf("day_of_week ranges using 7 are not supported in portable cron")
		}
		value = 0
	}
	if value < spec.min || value > spec.max {
		return 0, fmt.Errorf("%s value %q out of range", spec.name, raw)
	}
	return value, nil
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
	calendar, err := normalizeCalendarFields(parts[0], parts[1], parts[2], parts[3], parts[4])
	if err != nil {
		return nil, err
	}
	calendar.Cron = raw
	return calendar, nil
}

func normalizeCalendar(calendar *CalendarSpec, legacyCron string) (*CalendarSpec, error) {
	if calendar == nil {
		if strings.TrimSpace(legacyCron) == "" {
			return nil, fmt.Errorf("calendar spec cannot be empty")
		}
		return parseCronCalendar(legacyCron)
	}

	rawCron := strings.TrimSpace(calendar.Cron)
	if rawCron == "" && strings.TrimSpace(legacyCron) != "" {
		rawCron = strings.TrimSpace(legacyCron)
	}
	minute := strings.TrimSpace(calendar.Minute)
	hour := strings.TrimSpace(calendar.Hour)
	dayOfMonth := strings.TrimSpace(calendar.DayOfMonth)
	month := strings.TrimSpace(calendar.Month)
	dayOfWeek := strings.TrimSpace(calendar.DayOfWeek)
	if minute == "" || hour == "" || dayOfMonth == "" || month == "" || dayOfWeek == "" {
		if rawCron == "" {
			return nil, fmt.Errorf("calendar spec requires minute, hour, day_of_month, month, and day_of_week")
		}
		return parseCronCalendar(rawCron)
	}
	normalized, err := normalizeCalendarFields(minute, hour, dayOfMonth, month, dayOfWeek)
	if err != nil {
		return nil, err
	}
	normalized.Cron = rawCron
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
	trimmed := strings.TrimSpace(raw)
	switch strings.ToLower(trimmed) {
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
		if strings.HasPrefix(trimmed, "@") {
			return "", fmt.Errorf("cron descriptor %s is not supported", trimmed)
		}
		return raw, nil
	}
}

func nextScheduleRun(sc Schedule, after time.Time, loc *time.Location) (time.Time, error) {
	switch sc.Spec.Trigger {
	case TriggerInterval:
		return nextIntervalRun(sc.Spec.Interval, after)
	case TriggerCalendar:
		return nextCalendarRun(sc.Spec.Calendar, after, loc)
	case TriggerCron:
		return nextCalendarRun(sc.Spec.Calendar, after, loc)
	default:
		return time.Time{}, fmt.Errorf("trigger %s does not support next run calculation", sc.Spec.Trigger)
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
