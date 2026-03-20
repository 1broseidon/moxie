package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Artifact is lightweight metadata for a subagent run.
// The full result lives in the thread store; this is an index entry.
type Artifact struct {
	ID        string    `json:"id"`
	JobID     string    `json:"job_id"`
	Backend   string    `json:"backend"`
	Task      string    `json:"task"`
	ThreadID  string    `json:"thread_id"`
	ParentJob string    `json:"parent_job,omitempty"`
	Created   time.Time `json:"created"`
}

func artifactDir() string {
	dir := filepath.Join(ConfigDir(), "artifacts")
	_ = os.MkdirAll(dir, 0700)
	return dir
}

func artifactPath(id string) string {
	return filepath.Join(artifactDir(), id+".json")
}

// WriteArtifact persists an artifact to disk.
func WriteArtifact(a Artifact) error {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal artifact: %w", err)
	}
	return os.WriteFile(artifactPath(a.ID), data, 0600)
}

// ReadArtifact loads a single artifact by ID.
func ReadArtifact(id string) (Artifact, bool) {
	data, err := os.ReadFile(artifactPath(id))
	if err != nil {
		return Artifact{}, false
	}
	var a Artifact
	if err := json.Unmarshal(data, &a); err != nil {
		return Artifact{}, false
	}
	return a, true
}

// ListArtifacts returns all artifacts sorted by creation time (newest first).
func ListArtifacts() []Artifact {
	entries, err := os.ReadDir(artifactDir())
	if err != nil {
		return nil
	}
	var artifacts []Artifact
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-5]
		if a, ok := ReadArtifact(id); ok {
			artifacts = append(artifacts, a)
		}
	}
	sort.Slice(artifacts, func(i, j int) bool {
		return artifacts[i].Created.After(artifacts[j].Created)
	})
	return artifacts
}

// NewArtifactID generates a unique artifact ID.
func NewArtifactID() string {
	return fmt.Sprintf("art-%d", time.Now().UnixNano())
}
