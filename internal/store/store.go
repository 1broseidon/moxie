package store

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

type Config struct {
	Channels   map[string]ChannelConfig `json:"channels,omitempty"`
	Workspaces map[string]string        `json:"workspaces,omitempty"`
}

type ChannelConfig struct {
	Provider  string `json:"provider"`
	Token     string `json:"token,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
}

type configFile struct {
	Channels   map[string]ChannelConfig `json:"channels,omitempty"`
	Workspaces map[string]string        `json:"workspaces,omitempty"`
	Token      string                   `json:"token,omitempty"`
	ChatID     int64                    `json:"chat_id,omitempty"`
}

var cfgDir string

func ConfigDir() string {
	if cfgDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			panic(fmt.Sprintf("cannot determine home directory: %v", err))
		}
		cfgDir = filepath.Join(home, ".config", "moxie")
	}
	return cfgDir
}

func SetConfigDir(dir string) func() {
	prev := cfgDir
	cfgDir = dir
	return func() {
		cfgDir = prev
	}
}

func LoadConfig() (Config, error) {
	var file configFile
	if err := ReadJSON("config.json", &file); err != nil {
		return Config{}, fmt.Errorf("config not found: %w\nRun: moxie init", err)
	}
	cfg := Config{
		Channels:   file.Channels,
		Workspaces: file.Workspaces,
	}
	if cfg.Workspaces == nil {
		cfg.Workspaces = map[string]string{}
	}
	if cfg.Channels == nil {
		cfg.Channels = map[string]ChannelConfig{}
	}
	if len(cfg.Channels) == 0 && file.Token != "" && file.ChatID != 0 {
		cfg.Channels["telegram"] = ChannelConfig{
			Provider:  "telegram",
			Token:     file.Token,
			ChannelID: fmt.Sprintf("%d", file.ChatID),
		}
	}
	if _, err := cfg.Telegram(); err != nil {
		return Config{}, fmt.Errorf("%w\nRun: moxie init", err)
	}
	return cfg, nil
}

func SaveConfig(cfg Config) {
	if cfg.Channels == nil {
		cfg.Channels = map[string]ChannelConfig{}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	Check(err)
	Check(os.WriteFile(ConfigFile("config.json"), data, 0600))
}

func (cfg Config) Telegram() (ChannelConfig, error) {
	tg, ok := cfg.Channels["telegram"]
	if !ok {
		return ChannelConfig{}, fmt.Errorf("config missing telegram channel")
	}
	if tg.Provider == "" {
		tg.Provider = "telegram"
	}
	if tg.Token == "" {
		return ChannelConfig{}, fmt.Errorf("config missing telegram token")
	}
	if tg.ChannelID == "" {
		return ChannelConfig{}, fmt.Errorf("config missing telegram channel_id")
	}
	return tg, nil
}

type State struct {
	Backend  string `json:"backend"`
	Model    string `json:"model,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
	CWD      string `json:"cwd,omitempty"`
}

type PendingJob struct {
	ID             string    `json:"id"`
	SourceEventID  string    `json:"source_event_id,omitempty"`
	ScheduleID     string    `json:"schedule_id,omitempty"`
	ConversationID string    `json:"conversation_id"`
	Source         string    `json:"source,omitempty"`
	Prompt         string    `json:"prompt"`
	CWD            string    `json:"cwd,omitempty"`
	TempPath       string    `json:"temp_path,omitempty"`
	State          State     `json:"state"`
	Status         string    `json:"status"`
	Result         string    `json:"result,omitempty"`
	Updated        time.Time `json:"updated"`
}

func ConfigFile(name string) string {
	return filepath.Join(ConfigDir(), name)
}

func ReadJSON(name string, v any) error {
	data, err := os.ReadFile(ConfigFile(name))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func WriteJSON(name string, v any) {
	data, err := json.Marshal(v)
	Check(err)
	Check(os.WriteFile(ConfigFile(name), data, 0600))
}

func ReadState() State {
	var s State
	ReadJSON("state.json", &s)
	if s.Backend == "" {
		s.Backend = "claude"
	}
	if s.ThreadID == "" {
		s.ThreadID = "chat"
	}
	return s
}

func WriteState(s State) { WriteJSON("state.json", s) }

func JobsDir() string {
	return filepath.Join(ConfigDir(), "jobs")
}

func JobFile(jobID string) string {
	return filepath.Join(JobsDir(), jobID+".json")
}

func WriteJob(job PendingJob) {
	if job.ID == "" {
		job.ID = NewJobID()
	}
	job.Updated = time.Now()
	Check(os.MkdirAll(JobsDir(), 0700))
	data, err := json.Marshal(job)
	Check(err)
	Check(os.WriteFile(JobFile(job.ID), data, 0600))
}

func RemoveJob(jobID string) {
	err := os.Remove(JobFile(jobID))
	if err != nil && !os.IsNotExist(err) {
		log.Printf("error: remove job %s: %v", jobID, err)
	}
}

func JobExists(jobID string) bool {
	_, err := os.Stat(JobFile(jobID))
	return err == nil
}

func CleanupJobTemp(job PendingJob) {
	if job.TempPath == "" {
		return
	}
	if err := os.Remove(job.TempPath); err != nil && !os.IsNotExist(err) {
		log.Printf("temp file cleanup error for %s: %v", job.TempPath, err)
	}
}

func ListJobs() []PendingJob {
	entries, err := os.ReadDir(JobsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		log.Printf("error: read jobs dir: %v", err)
		return nil
	}
	jobs := make([]PendingJob, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(JobsDir(), entry.Name()))
		if err != nil {
			log.Printf("error: read job %s: %v", entry.Name(), err)
			continue
		}
		var job PendingJob
		if err := json.Unmarshal(data, &job); err != nil {
			log.Printf("error: parse job %s: %v", entry.Name(), err)
			continue
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		iID, iErr := strconv.Atoi(jobs[i].SourceEventID)
		jID, jErr := strconv.Atoi(jobs[j].SourceEventID)
		if iErr == nil && jErr == nil && jobs[i].Source != "" && jobs[i].Source == jobs[j].Source {
			return iID < jID
		}
		if jobs[i].Updated.Equal(jobs[j].Updated) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].Updated.Before(jobs[j].Updated)
	})
	return jobs
}

func NewJobID() string {
	return fmt.Sprintf("job-%d", time.Now().UnixNano())
}

func Check(err error) {
	if err != nil {
		log.Printf("error: %v", err)
	}
}
