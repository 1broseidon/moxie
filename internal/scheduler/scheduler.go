package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

type Trigger string

const (
	TriggerAt   Trigger = "at"
	TriggerCron Trigger = "cron"
)

type Action string

const (
	ActionSend     Action = "send"
	ActionDispatch Action = "dispatch"
)

type Schedule struct {
	ID           string    `json:"id"`
	Trigger      Trigger   `json:"trigger"`
	Action       Action    `json:"action"`
	At           time.Time `json:"at,omitempty"`
	Cron         string    `json:"cron,omitempty"`
	Text         string    `json:"text"`
	Backend      string    `json:"backend,omitempty"`
	Model        string    `json:"model,omitempty"`
	ThreadID     string    `json:"thread_id,omitempty"`
	CWD          string    `json:"cwd,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	NextRun      time.Time `json:"next_run"`
	LastRun      time.Time `json:"last_run,omitempty"`
	RunningJobID int       `json:"running_job_id,omitempty"`
}

type AddInput struct {
	Trigger  Trigger
	Action   Action
	At       string
	In       string
	Cron     string
	Text     string
	Backend  string
	Model    string
	ThreadID string
	CWD      string
	Now      time.Time
}

type Store struct {
	path string
	loc  *time.Location
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
	schedules, err := s.load()
	if err != nil {
		return nil, err
	}
	sortSchedules(schedules)
	return schedules, nil
}

func (s *Store) Get(id string) (Schedule, error) {
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
	schedules, err := s.load()
	if err != nil {
		return err
	}
	next := schedules[:0]
	found := false
	for _, sc := range schedules {
		if sc.ID == id {
			if sc.RunningJobID != 0 {
				return fmt.Errorf("schedule %s is running via job %d", id, sc.RunningJobID)
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
	schedules, err := s.load()
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().In(s.loc)
	}
	due := make([]Schedule, 0)
	for _, sc := range schedules {
		if sc.RunningJobID != 0 {
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

func (s *Store) AttachJob(id string, jobID int) (Schedule, error) {
	if jobID == 0 {
		return Schedule{}, fmt.Errorf("job id cannot be zero")
	}
	schedules, err := s.load()
	if err != nil {
		return Schedule{}, err
	}
	for i, sc := range schedules {
		if sc.ID != id {
			continue
		}
		if sc.RunningJobID != 0 && sc.RunningJobID != jobID {
			return Schedule{}, fmt.Errorf("schedule %s already attached to job %d", id, sc.RunningJobID)
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

func (s *Store) MarkDone(id string, jobID int, finishedAt time.Time) (Schedule, error) {
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
		if sc.RunningJobID != 0 && sc.RunningJobID != jobID {
			return Schedule{}, fmt.Errorf("schedule %s attached to different job %d", id, sc.RunningJobID)
		}
		sc.RunningJobID = 0
		sc.LastRun = finishedAt
		if sc.Trigger == TriggerAt {
			next := append(schedules[:i:i], schedules[i+1:]...)
			if err := s.save(next); err != nil {
				return Schedule{}, err
			}
			return sc, nil
		}
		nextRun, err := nextCronRun(sc.Cron, finishedAt, s.loc)
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

func (s *Store) Repair(jobExists func(int) bool) error {
	schedules, err := s.load()
	if err != nil {
		return err
	}
	changed := false
	for i, sc := range schedules {
		if sc.RunningJobID == 0 {
			continue
		}
		if jobExists(sc.RunningJobID) {
			continue
		}
		sc.RunningJobID = 0
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

	trigger := input.Trigger
	if trigger != TriggerAt && trigger != TriggerCron {
		return Schedule{}, fmt.Errorf("trigger must be at or cron")
	}

	sc := Schedule{
		ID:        newID(now),
		Trigger:   trigger,
		Action:    action,
		Text:      text,
		Backend:   strings.TrimSpace(input.Backend),
		Model:     strings.TrimSpace(input.Model),
		ThreadID:  strings.TrimSpace(input.ThreadID),
		CWD:       strings.TrimSpace(input.CWD),
		CreatedAt: now,
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
	case TriggerCron:
		spec := strings.TrimSpace(input.Cron)
		nextRun, err := nextCronRun(spec, now, s.loc)
		if err != nil {
			return Schedule{}, err
		}
		sc.Cron = spec
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
	sortSchedules(doc.Schedules)
	return doc.Schedules, nil
}

func (s *Store) save(schedules []Schedule) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	sortSchedules(schedules)
	data, err := json.MarshalIndent(fileData{Schedules: schedules}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

func sortSchedules(schedules []Schedule) {
	sort.Slice(schedules, func(i, j int) bool {
		if schedules[i].NextRun.Equal(schedules[j].NextRun) {
			return schedules[i].CreatedAt.Before(schedules[j].CreatedAt)
		}
		return schedules[i].NextRun.Before(schedules[j].NextRun)
	})
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
	var firstErr error
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
		if firstErr == nil {
			firstErr = err
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

func nextCronRun(spec string, after time.Time, loc *time.Location) (time.Time, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
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
		return time.Time{}, errors.New("cron schedule has no next run")
	}
	return next, nil
}

func compactText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}
