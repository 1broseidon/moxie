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
	Channels         map[string]ChannelConfig `json:"channels,omitempty"`
	Workspaces       map[string]string        `json:"workspaces,omitempty"`
	DefaultCWD       string                   `json:"default_cwd,omitempty"`
	SubagentMaxDepth int                      `json:"subagent_max_depth,omitempty"`
}

type ChannelConfig struct {
	Provider  string `json:"provider"`
	Token     string `json:"token,omitempty"`
	AppToken  string `json:"app_token,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
}

type configFile struct {
	Channels         map[string]ChannelConfig `json:"channels,omitempty"`
	Workspaces       map[string]string        `json:"workspaces,omitempty"`
	DefaultCWD       string                   `json:"default_cwd,omitempty"`
	SubagentMaxDepth int                      `json:"subagent_max_depth,omitempty"`
	Token            string                   `json:"token,omitempty"`
	ChatID           int64                    `json:"chat_id,omitempty"`
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
		Channels:         file.Channels,
		Workspaces:       file.Workspaces,
		DefaultCWD:       file.DefaultCWD,
		SubagentMaxDepth: file.SubagentMaxDepth,
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
	if !cfg.hasValidChannel() {
		return Config{}, fmt.Errorf("config missing at least one valid channel\nRun: moxie init")
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
	tg, err := cfg.channel("telegram")
	if err != nil {
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

func (cfg Config) Slack() (ChannelConfig, error) {
	slack, err := cfg.channel("slack")
	if err != nil {
		return ChannelConfig{}, fmt.Errorf("config missing slack channel")
	}
	if slack.Provider == "" {
		slack.Provider = "slack"
	}
	if slack.Token == "" {
		return ChannelConfig{}, fmt.Errorf("config missing slack token")
	}
	if slack.AppToken == "" {
		return ChannelConfig{}, fmt.Errorf("config missing slack app_token")
	}
	return slack, nil
}

func (cfg Config) MaxSubagentDepth() int {
	if cfg.SubagentMaxDepth > 0 {
		return cfg.SubagentMaxDepth
	}
	return 3
}

func (cfg Config) channel(name string) (ChannelConfig, error) {
	c, ok := cfg.Channels[name]
	if !ok {
		return ChannelConfig{}, fmt.Errorf("config missing %s channel", name)
	}
	return c, nil
}

func (cfg Config) hasValidChannel() bool {
	for name, ch := range cfg.Channels {
		if channelIsValid(name, ch) {
			return true
		}
	}
	return false
}

func channelProvider(name string, ch ChannelConfig) string {
	if ch.Provider != "" {
		return ch.Provider
	}
	return name
}

func channelIsValid(name string, ch ChannelConfig) bool {
	switch channelProvider(name, ch) {
	case "telegram":
		return ch.Token != "" && ch.ChannelID != ""
	case "slack":
		return ch.Token != "" && ch.AppToken != ""
	default:
		return false
	}
}

type State struct {
	Backend  string `json:"backend"`
	Model    string `json:"model,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
	CWD      string `json:"cwd,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

type stateFile struct {
	Default       State            `json:"default,omitempty"`
	Conversations map[string]State `json:"conversations,omitempty"`
}

type PendingJob struct {
	ID                string    `json:"id"`
	SourceEventID     string    `json:"source_event_id,omitempty"`
	ScheduleID        string    `json:"schedule_id,omitempty"`
	ParentJobID       string    `json:"parent_job_id,omitempty"`
	DelegatedTask     string    `json:"delegated_task,omitempty"`
	DelegationContext string    `json:"delegation_context,omitempty"`
	ReplyConversation string    `json:"reply_conversation,omitempty"`
	Depth             int       `json:"depth,omitempty"`
	ConversationID    string    `json:"conversation_id"`
	Source            string    `json:"source,omitempty"`
	Prompt            string    `json:"prompt"`
	CWD               string    `json:"cwd,omitempty"`
	TempPath          string    `json:"temp_path,omitempty"`
	SynthesisState    State     `json:"synthesis_state,omitempty"`
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

func normalizeState(s State) State {
	if s.Backend == "" {
		s.Backend = "claude"
	}
	if s.ThreadID == "" {
		s.ThreadID = "chat"
	}
	return s
}

func loadStateFile() stateFile {
	var doc stateFile
	data, err := os.ReadFile(ConfigFile("state.json"))
	if err != nil {
		return stateFile{Conversations: map[string]State{}}
	}
	if err := json.Unmarshal(data, &doc); err == nil && (doc.Default != (State{}) || len(doc.Conversations) > 0) {
		if doc.Conversations == nil {
			doc.Conversations = map[string]State{}
		}
		doc.Default = normalizeState(doc.Default)
		for id, st := range doc.Conversations {
			doc.Conversations[id] = normalizeState(st)
		}
		return doc
	}

	var legacy State
	if err := json.Unmarshal(data, &legacy); err == nil {
		return stateFile{
			Default:       normalizeState(legacy),
			Conversations: map[string]State{},
		}
	}

	log.Printf("error: parse state.json")
	return stateFile{Conversations: map[string]State{}}
}

func saveStateFile(doc stateFile) {
	if doc.Conversations == nil {
		doc.Conversations = map[string]State{}
	}
	doc.Default = normalizeState(doc.Default)
	for id, st := range doc.Conversations {
		doc.Conversations[id] = normalizeState(st)
	}
	WriteJSON("state.json", doc)
}

func ReadState() State {
	return normalizeState(loadStateFile().Default)
}

func WriteState(s State) {
	doc := loadStateFile()
	doc.Default = normalizeState(s)
	saveStateFile(doc)
}

func ReadConversationState(conversationID string) State {
	doc := loadStateFile()
	if conversationID != "" {
		if st, ok := doc.Conversations[conversationID]; ok {
			return normalizeState(st)
		}
	}
	if doc.Default == (State{}) {
		return normalizeState(State{})
	}
	return normalizeState(doc.Default)
}

func WriteConversationState(conversationID string, s State) {
	if conversationID == "" {
		WriteState(s)
		return
	}
	doc := loadStateFile()
	if doc.Conversations == nil {
		doc.Conversations = map[string]State{}
	}
	doc.Conversations[conversationID] = normalizeState(s)
	saveStateFile(doc)
}

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

func ReadJob(jobID string) (PendingJob, bool) {
	data, err := os.ReadFile(JobFile(jobID))
	if err != nil {
		return PendingJob{}, false
	}
	var job PendingJob
	if err := json.Unmarshal(data, &job); err != nil {
		log.Printf("error: parse job %s: %v", jobID, err)
		return PendingJob{}, false
	}
	return job, true
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
