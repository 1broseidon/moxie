package store

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Token      string            `json:"token"`
	ChatID     int64             `json:"chat_id"`
	Workspaces map[string]string `json:"workspaces,omitempty"`
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
	var cfg Config
	if err := ReadJSON("config.json", &cfg); err != nil {
		return Config{}, fmt.Errorf("config not found: %w\nRun: moxie init", err)
	}
	if cfg.Token == "" {
		return Config{}, fmt.Errorf("config missing token\nRun: moxie init")
	}
	if cfg.ChatID == 0 {
		return Config{}, fmt.Errorf("config missing chat_id\nRun: moxie init")
	}
	if cfg.Workspaces == nil {
		cfg.Workspaces = map[string]string{}
	}
	return cfg, nil
}

func SaveConfig(cfg Config) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	Check(err)
	Check(os.WriteFile(ConfigFile("config.json"), data, 0600))
}

type State struct {
	Backend  string `json:"backend"`
	Model    string `json:"model,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
	CWD      string `json:"cwd,omitempty"`
}

type PendingJob struct {
	UpdateID          int       `json:"update_id"`
	ScheduleID        string    `json:"schedule_id,omitempty"`
	ChatID            int64     `json:"chat_id"`
	Prompt            string    `json:"prompt"`
	CWD               string    `json:"cwd,omitempty"`
	TempPath          string    `json:"temp_path,omitempty"`
	StatusMessageID   int       `json:"status_message_id,omitempty"`
	StatusMessageHTML string    `json:"status_message_html,omitempty"`
	State             State     `json:"state"`
	Status            string    `json:"status"`
	Result            string    `json:"result,omitempty"`
	Updated           time.Time `json:"updated"`
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
		s.ThreadID = "telegram"
	}
	return s
}

func WriteState(s State) { WriteJSON("state.json", s) }

func JobsDir() string {
	return filepath.Join(ConfigDir(), "jobs")
}

func JobFile(updateID int) string {
	return filepath.Join(JobsDir(), strconv.Itoa(updateID)+".json")
}

func WriteJob(job PendingJob) {
	job.Updated = time.Now()
	Check(os.MkdirAll(JobsDir(), 0700))
	data, err := json.Marshal(job)
	Check(err)
	Check(os.WriteFile(JobFile(job.UpdateID), data, 0600))
}

func RemoveJob(updateID int) {
	err := os.Remove(JobFile(updateID))
	if err != nil && !os.IsNotExist(err) {
		log.Printf("error: remove job %d: %v", updateID, err)
	}
}

func JobExists(updateID int) bool {
	_, err := os.Stat(JobFile(updateID))
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
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].UpdateID < jobs[j].UpdateID })
	return jobs
}

func CursorOffset() int {
	if c := ReadCursor(); c > 0 {
		return c + 1
	}
	return 0
}

func ReadCursor() int {
	data, err := os.ReadFile(ConfigFile("cursor"))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		log.Printf("corrupt cursor file, resetting: %v", err)
		return 0
	}
	return n
}

func WriteCursor(id int) {
	Check(os.WriteFile(ConfigFile("cursor"), []byte(strconv.Itoa(id)), 0600))
}

func Check(err error) {
	if err != nil {
		log.Printf("error: %v", err)
	}
}
