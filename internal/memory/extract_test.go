package memory

import (
	"testing"

	"github.com/1broseidon/moxie/internal/store"
)

func TestParseExtractionValidJSON(t *testing.T) {
	resp := `{"facts": [{"text": "User prefers SQLite over Postgres", "kind": "preference"}, {"text": "Auth rewrite uses approach B", "kind": "project"}]}`
	facts, err := ParseExtraction(resp)
	if err != nil {
		t.Fatalf("ParseExtraction: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(facts))
	}
	if facts[0].Kind != "preference" {
		t.Fatalf("expected preference, got %q", facts[0].Kind)
	}
	if facts[1].Kind != "project" {
		t.Fatalf("expected project, got %q", facts[1].Kind)
	}
}

func TestParseExtractionEmptyFacts(t *testing.T) {
	resp := `{"facts": []}`
	facts, err := ParseExtraction(resp)
	if err != nil {
		t.Fatalf("ParseExtraction: %v", err)
	}
	if len(facts) != 0 {
		t.Fatalf("expected 0 facts, got %d", len(facts))
	}
}

func TestParseExtractionProseAroundJSON(t *testing.T) {
	resp := `Based on the messages, here's what I found:

{"facts": [{"text": "User wants real integration tests", "kind": "preference"}]}

That's the only relevant memory.`
	facts, err := ParseExtraction(resp)
	if err != nil {
		t.Fatalf("ParseExtraction: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
}

func TestParseExtractionInvalidKindDefaultsToPreference(t *testing.T) {
	resp := `{"facts": [{"text": "something important", "kind": "unknown_kind"}]}`
	facts, err := ParseExtraction(resp)
	if err != nil {
		t.Fatalf("ParseExtraction: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if facts[0].Kind != "preference" {
		t.Fatalf("expected preference default, got %q", facts[0].Kind)
	}
}

func TestParseExtractionNoJSON(t *testing.T) {
	resp := `Nothing to save.`
	_, err := ParseExtraction(resp)
	if err == nil {
		t.Fatal("expected error for no JSON")
	}
}

func TestParseExtractionSkipsEmptyText(t *testing.T) {
	resp := `{"facts": [{"text": "", "kind": "preference"}, {"text": "real fact", "kind": "project"}]}`
	facts, err := ParseExtraction(resp)
	if err != nil {
		t.Fatalf("ParseExtraction: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact (empty skipped), got %d", len(facts))
	}
}

func TestFormatBatch(t *testing.T) {
	batch := []bufferedPrompt{
		{Text: "first message"},
		{Text: "second message"},
	}
	result := formatBatch(batch)
	if result == "" {
		t.Fatal("expected non-empty batch format")
	}
	if !contains(result, "Message 1") || !contains(result, "Message 2") {
		t.Fatalf("expected message numbers in output, got: %s", result)
	}
	if !contains(result, "first message") || !contains(result, "second message") {
		t.Fatalf("expected message text in output, got: %s", result)
	}
}

func TestBufferAccumulatesAndFlushes(t *testing.T) {
	buf := &promptBuffer{capacity: 3}

	// First two adds return nil (not full yet).
	if batch := buf.add(bufferedPrompt{Text: "one"}); batch != nil {
		t.Fatal("expected nil before capacity")
	}
	if batch := buf.add(bufferedPrompt{Text: "two"}); batch != nil {
		t.Fatal("expected nil before capacity")
	}

	// Third add triggers flush.
	batch := buf.add(bufferedPrompt{Text: "three"})
	if batch == nil {
		t.Fatal("expected batch at capacity")
	}
	if len(batch) != 3 {
		t.Fatalf("expected 3 items, got %d", len(batch))
	}

	// Buffer should be empty now.
	flushed := buf.flush()
	if len(flushed) != 0 {
		t.Fatalf("expected empty after flush, got %d", len(flushed))
	}
}

func TestBufferFlushPartial(t *testing.T) {
	buf := &promptBuffer{capacity: 5}
	buf.add(bufferedPrompt{Text: "one"})
	buf.add(bufferedPrompt{Text: "two"})

	batch := buf.flush()
	if len(batch) != 2 {
		t.Fatalf("expected 2 partial items, got %d", len(batch))
	}
}

func TestCaptureBuffersWhenExtractFuncSet(t *testing.T) {
	s := setupTestStore(t)
	writeTestConfig(t, "on")

	// Reset global buffer for test isolation.
	globalBuffer = &promptBuffer{capacity: 3}
	defer func() { globalBuffer = &promptBuffer{capacity: DefaultBatchSize} }()

	callCount := 0
	ExtractFunc = func(text string) ([]Fact, error) {
		callCount++
		return []Fact{{Text: "extracted fact", Kind: "preference"}}, nil
	}
	defer func() { ExtractFunc = nil }()

	job := store.PendingJob{
		Prompt: "I prefer sqlite over postgres",
		Source: "telegram",
		ID:     "job1",
	}

	// First two captures should buffer silently.
	if err := Capture(s, job); err != nil {
		t.Fatalf("Capture 1: %v", err)
	}
	if callCount != 0 {
		t.Fatalf("expected 0 LLM calls after 1 message, got %d", callCount)
	}

	job.ID = "job2"
	if err := Capture(s, job); err != nil {
		t.Fatalf("Capture 2: %v", err)
	}
	if callCount != 0 {
		t.Fatalf("expected 0 LLM calls after 2 messages, got %d", callCount)
	}

	// Third capture triggers extraction.
	job.ID = "job3"
	if err := Capture(s, job); err != nil {
		t.Fatalf("Capture 3: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 LLM call after batch full, got %d", callCount)
	}

	// Verify fact was stored.
	n, _ := s.Count()
	if n != 1 {
		t.Fatalf("expected 1 stored fact, got %d", n)
	}
}

func TestCaptureUsesHeuristicWhenNoExtractFunc(t *testing.T) {
	s := setupTestStore(t)
	writeTestConfig(t, "on")

	// Ensure ExtractFunc is nil.
	ExtractFunc = nil

	job := store.PendingJob{
		Prompt: "I prefer minimalist systems over heavy frameworks",
		Source: "telegram",
		ID:     "job-heuristic",
	}

	if err := Capture(s, job); err != nil {
		t.Fatalf("Capture: %v", err)
	}

	// Heuristic should have caught "I prefer".
	n, _ := s.Count()
	if n == 0 {
		t.Fatal("expected heuristic to capture a fact")
	}
}

func TestCaptureSkipsOffMode(t *testing.T) {
	s := setupTestStore(t)
	writeTestConfig(t, "off")

	callCount := 0
	ExtractFunc = func(text string) ([]Fact, error) {
		callCount++
		return nil, nil
	}
	defer func() { ExtractFunc = nil }()

	job := store.PendingJob{
		Prompt: "I prefer everything",
		Source: "telegram",
		ID:     "job-off",
	}
	Capture(s, job)

	if callCount != 0 {
		t.Fatal("ExtractFunc should not be called in off mode")
	}
}

func TestFlushBufferOnShutdown(t *testing.T) {
	s := setupTestStore(t)
	writeTestConfig(t, "on")

	globalBuffer = &promptBuffer{capacity: 10}
	defer func() { globalBuffer = &promptBuffer{capacity: DefaultBatchSize} }()

	callCount := 0
	ExtractFunc = func(text string) ([]Fact, error) {
		callCount++
		return []Fact{{Text: "flushed fact", Kind: "preference"}}, nil
	}
	defer func() { ExtractFunc = nil }()

	// Add some messages but don't fill the buffer.
	job := store.PendingJob{Prompt: "I always use Go", Source: "telegram", ID: "flush1"}
	Capture(s, job)
	job.ID = "flush2"
	Capture(s, job)

	if callCount != 0 {
		t.Fatal("should not have called LLM yet")
	}

	// Simulate shutdown flush.
	FlushBuffer(s)

	if callCount != 1 {
		t.Fatalf("expected 1 LLM call on flush, got %d", callCount)
	}

	n, _ := s.Count()
	if n != 1 {
		t.Fatalf("expected 1 stored fact after flush, got %d", n)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
