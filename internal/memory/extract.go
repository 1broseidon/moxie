package memory

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// ExtractionPrompt is the system-level instruction for the memory gate LLM.
// Designed so that "nothing worth storing" is the default outcome.
const ExtractionPrompt = `You are a memory gate. You receive a batch of recent user messages from a chat assistant called Moxie. Your job: decide if any contain facts worth recalling in a DIFFERENT conversation weeks or months from now.

Return ONLY a JSON object: {"facts": []}
An EMPTY array is the correct answer 80-90% of the time.

A fact is worth storing ONLY if ALL of these are true:
1. It expresses a preference, decision, correction, or non-obvious project fact
2. Forgetting it would cause the assistant to make a mistake or ask again
3. It cannot be derived from the codebase, git history, or documentation

EXTRACT:
- "I prefer X over Y" / "don't use X" / "always do Y" → preference
- "We decided to go with approach B" / "the project uses X" → project
- "Remember that X" / "keep in mind Y" → reference
- "I learned X" / "from now on do Y" → growth
- Corrections: "no not that — do X instead" → preference (most valuable signal)

DO NOT EXTRACT:
- Task instructions ("fix the bug", "deploy to staging")
- Questions ("what does this do?")
- Conversational filler ("ok", "sounds good", "thanks")
- Facts derivable from code or git ("the API uses REST")
- Architecture or file paths (read the code)
- What the assistant said or did (only capture USER signal)

For each fact, return:
{"text": "<distilled fact — not a verbatim quote>", "kind": "<preference|project|reference|growth>"}

If nothing is worth storing, return {"facts": []}.
Silence is always better than noise.`

// DefaultBatchSize is the number of messages to accumulate before running
// LLM extraction. Configurable via memory_batch_size in config.json.
const DefaultBatchSize = 5

// bufferedPrompt holds a user message waiting for batch extraction.
type bufferedPrompt struct {
	Text     string
	Source   string
	JobID    string
	ThreadID string
	Backend  string
	CWD      string
	Time     time.Time
}

// promptBuffer accumulates recent user prompts for batch extraction.
type promptBuffer struct {
	mu       sync.Mutex
	items    []bufferedPrompt
	capacity int
}

// globalBuffer is the package-level prompt buffer.
var globalBuffer = &promptBuffer{capacity: DefaultBatchSize}

// SetBatchSize updates the buffer capacity. Must be called before capture starts.
func SetBatchSize(n int) {
	if n < 1 {
		n = 1
	}
	globalBuffer.mu.Lock()
	defer globalBuffer.mu.Unlock()
	globalBuffer.capacity = n
}

// add appends a prompt and returns the full batch if capacity is reached.
// Returns nil if the batch is not yet full.
func (b *promptBuffer) add(p bufferedPrompt) []bufferedPrompt {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items = append(b.items, p)
	if len(b.items) >= b.capacity {
		batch := b.items
		b.items = nil
		return batch
	}
	return nil
}

// flush drains whatever is in the buffer regardless of capacity.
func (b *promptBuffer) flush() []bufferedPrompt {
	b.mu.Lock()
	defer b.mu.Unlock()
	batch := b.items
	b.items = nil
	return batch
}

// FlushBuffer forces extraction on any buffered prompts. Call on shutdown.
func FlushBuffer(s *Store) {
	batch := globalBuffer.flush()
	if len(batch) == 0 || ExtractFunc == nil {
		return
	}
	if err := runBatchExtraction(s, batch); err != nil {
		log.Printf("memory: flush buffer error: %v", err)
	}
}

// formatBatch formats buffered prompts for the extraction LLM.
func formatBatch(batch []bufferedPrompt) string {
	var b strings.Builder
	for i, p := range batch {
		fmt.Fprintf(&b, "--- Message %d ---\n%s\n\n", i+1, strings.TrimSpace(p.Text))
	}
	return strings.TrimSpace(b.String())
}

// extractionResponse is the expected JSON structure from the extraction LLM.
type extractionResponse struct {
	Facts []Fact `json:"facts"`
}

// BatchSize returns the current buffer capacity.
func BatchSize() int {
	globalBuffer.mu.Lock()
	defer globalBuffer.mu.Unlock()
	return globalBuffer.capacity
}

// ParseExtraction finds and parses JSON from an LLM response.
// Tolerates prose around the JSON object.
func ParseExtraction(response string) ([]Fact, error) {
	response = strings.TrimSpace(response)
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object in response")
	}
	jsonStr := response[start : end+1]

	var resp extractionResponse
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return nil, fmt.Errorf("parse extraction JSON: %w", err)
	}

	// Validate kinds.
	valid := map[string]bool{"preference": true, "project": true, "reference": true, "growth": true}
	var filtered []Fact
	for _, f := range resp.Facts {
		f.Kind = strings.TrimSpace(strings.ToLower(f.Kind))
		if !valid[f.Kind] {
			f.Kind = "preference" // safe default
		}
		f.Text = strings.TrimSpace(f.Text)
		if f.Text == "" {
			continue
		}
		filtered = append(filtered, f)
	}
	return filtered, nil
}
