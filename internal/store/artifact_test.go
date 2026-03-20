package store

import (
	"strings"
	"testing"
	"time"
)

func TestArtifactWriteReadRoundtrip(t *testing.T) {
	cleanup := SetConfigDir(t.TempDir())
	defer cleanup()

	a := Artifact{
		ID:       NewArtifactID(),
		JobID:    "job-123",
		Source:   "subagent",
		Backend:  "codex",
		Task:     "Audit the scheduler",
		Result:   "Found 3 issues.",
		ThreadID: "bold-fox",
		Created:  time.Now(),
	}
	if err := WriteArtifact(a); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := ReadArtifact(a.ID)
	if !ok {
		t.Fatal("artifact not found after write")
	}
	if got.JobID != a.JobID || got.Result != a.Result || got.Task != a.Task {
		t.Fatalf("roundtrip mismatch: got %+v", got)
	}
}

func TestArtifactReadMissing(t *testing.T) {
	cleanup := SetConfigDir(t.TempDir())
	defer cleanup()

	_, ok := ReadArtifact("nonexistent")
	if ok {
		t.Fatal("expected not found for missing artifact")
	}
}

func TestListArtifactsSortedNewestFirst(t *testing.T) {
	cleanup := SetConfigDir(t.TempDir())
	defer cleanup()

	for i, name := range []string{"first", "second", "third"} {
		a := Artifact{
			ID:      NewArtifactID() + "-" + name,
			JobID:   "job-" + name,
			Source:  "dispatch",
			Backend: "claude",
			Task:    name,
			Result:  "result-" + name,
			Created: time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := WriteArtifact(a); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	artifacts := ListArtifacts()
	if len(artifacts) != 3 {
		t.Fatalf("expected 3 artifacts, got %d", len(artifacts))
	}
	if !strings.Contains(artifacts[0].Task, "third") {
		t.Fatalf("expected newest first, got %s", artifacts[0].Task)
	}
	if !strings.Contains(artifacts[2].Task, "first") {
		t.Fatalf("expected oldest last, got %s", artifacts[2].Task)
	}
}
