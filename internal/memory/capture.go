package memory

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/1broseidon/moxie/internal/store"
)

// memoryMode is read from config on each call so changes take effect without restart.
var configMu sync.Mutex

// capturable sources — only human-facing channels.
var captureSources = map[string]bool{
	"telegram": true,
	"slack":    true,
	"webex":    true,
}

// EmbedFunc is called to generate an embedding for a text string.
// Set this at startup to enable vector search. If nil, only FTS5 is used.
var EmbedFunc func(text string) ([]float32, error)

// ExtractFunc is called to extract structured facts from raw user text.
// Set this at startup to enable LLM-based extraction.
// If nil, falls back to heuristic extraction.
var ExtractFunc func(text string) ([]Fact, error)

// CurrentMode reads memory_mode from the Moxie config file.
func CurrentMode() Mode {
	configMu.Lock()
	defer configMu.Unlock()

	type memConfig struct {
		MemoryMode string `json:"memory_mode"`
	}
	var mc memConfig
	if err := store.ReadJSON("config.json", &mc); err != nil {
		return ModeOff
	}
	switch Mode(mc.MemoryMode) {
	case ModeDryRun:
		return ModeDryRun
	case ModeOn:
		return ModeOn
	default:
		return ModeOff
	}
}

// Capture is the post-delivery memory hook. When LLM extraction is configured,
// it buffers messages and flushes through the LLM when the batch is full.
// Otherwise it falls back to per-message heuristic extraction.
func Capture(s *Store, job store.PendingJob) error {
	mode := CurrentMode()
	if mode == ModeOff {
		return nil
	}
	if !captureSources[job.Source] {
		return nil
	}
	// Skip subagent/workflow jobs.
	if job.ParentJobID != "" || job.DelegatedTask != "" {
		return nil
	}
	if strings.TrimSpace(job.Prompt) == "" {
		return nil
	}

	// When LLM extraction is available, buffer messages for batch processing.
	if ExtractFunc != nil {
		bp := bufferedPrompt{
			Text:     job.Prompt,
			Source:   job.Source,
			JobID:    job.ID,
			ThreadID: job.State.ThreadID,
			Backend:  job.State.Backend,
			CWD:      job.CWD,
		}
		batch := globalBuffer.add(bp)
		if batch == nil {
			return nil // buffered, not yet at capacity
		}
		return runBatchExtraction(s, batch)
	}

	// Fallback: heuristic extraction per message.
	return captureWithFacts(s, heuristicExtract(job), job.CWD, job.Source, job.ID)
}

// runBatchExtraction sends a batch of buffered prompts through the LLM extractor.
// Falls back to heuristic extraction for each message if the LLM call fails.
func runBatchExtraction(s *Store, batch []bufferedPrompt) error {
	batchText := formatBatch(batch)
	facts, err := ExtractFunc(batchText)
	if err != nil {
		log.Printf("memory: LLM batch extraction failed, falling back to heuristic: %v", err)
		for _, bp := range batch {
			job := store.PendingJob{
				Prompt: bp.Text,
				Source: bp.Source,
				ID:     bp.JobID,
				CWD:    bp.CWD,
			}
			job.State.ThreadID = bp.ThreadID
			job.State.Backend = bp.Backend
			captureWithFacts(s, heuristicExtract(job), bp.CWD, bp.Source, bp.JobID)
		}
		return nil
	}

	if len(facts) == 0 {
		log.Printf("memory: batch extraction returned 0 facts from %d messages (expected)", len(batch))
		return nil
	}

	// Enrich with metadata from the last message in the batch.
	last := batch[len(batch)-1]
	for i := range facts {
		if facts[i].Source == "" {
			facts[i].Source = last.Source
		}
		if facts[i].JobID == "" {
			facts[i].JobID = last.JobID
		}
		if facts[i].ThreadID == "" {
			facts[i].ThreadID = last.ThreadID
		}
		if facts[i].Backend == "" {
			facts[i].Backend = last.Backend
		}
		if len(facts[i].Tags) == 0 {
			facts[i].Tags = deriveTags(facts[i].Text, facts[i].Kind)
		}
	}

	return captureWithFacts(s, facts, last.CWD, last.Source, last.JobID)
}

// captureWithFacts is the shared storage path for both LLM and heuristic extraction.
func captureWithFacts(s *Store, facts []Fact, cwd, source, jobID string) error {
	if len(facts) == 0 {
		return nil
	}

	project := normalizeProjectPath(cwd)
	for i := range facts {
		facts[i].Scope = scopeForKind(facts[i].Kind)
		if facts[i].Scope == "project" {
			facts[i].Project = project
		} else {
			facts[i].Project = ""
		}
	}

	stored := 0
	for _, fact := range facts {
		dup, err := s.IsDuplicate(fact.Text, fact.Project)
		if err != nil {
			log.Printf("memory: dedup check error: %v", err)
			continue
		}
		if dup {
			continue
		}

		if EmbedFunc != nil {
			emb, err := EmbedFunc(fact.Text)
			if err != nil {
				log.Printf("memory: embedding error (non-fatal): %v", err)
			} else {
				fact.Embedding = emb
			}
		}

		if _, err := s.Add(fact); err != nil {
			log.Printf("memory: store error: %v", err)
			continue
		}
		stored++
	}

	if stored > 0 {
		log.Printf("memory: captured %d fact(s) from %s job %s [mode=%s]", stored, source, jobID, CurrentMode())
	}
	return nil
}

func scopeForKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case "project", "reference":
		return "project"
	case "preference", "growth":
		return "global"
	default:
		return "global"
	}
}

// heuristicExtract is the fallback when no LLM extraction is configured.
// It classifies segments of the user prompt using keyword patterns.
func heuristicExtract(job store.PendingJob) []Fact {
	segments := splitSegments(job.Prompt)
	var facts []Fact

	for _, seg := range segments {
		kind, ok := classifySegment(seg)
		if !ok {
			continue
		}
		facts = append(facts, Fact{
			Text:     seg,
			Kind:     kind,
			Tags:     deriveTags(seg, kind),
			Source:   job.Source,
			JobID:    job.ID,
			ThreadID: job.State.ThreadID,
			Backend:  job.State.Backend,
		})
	}
	return facts
}

func splitSegments(text string) []string {
	// Split on sentence boundaries.
	text = strings.ReplaceAll(text, "!", ".")
	text = strings.ReplaceAll(text, "?", ".")
	text = strings.ReplaceAll(text, ";", ".")

	var segments []string
	for _, part := range strings.Split(text, ".") {
		for _, line := range strings.Split(part, "\n") {
			s := strings.TrimSpace(line)
			if len(s) >= 10 && len(s) <= 300 {
				segments = append(segments, s)
			}
		}
	}
	return segments
}

func classifySegment(seg string) (string, bool) {
	lower := strings.ToLower(seg)

	prefNeedles := []string{
		"i prefer", "i like", "i love", "i dislike", "i hate",
		"i want", "i don't want", "i don't like", "i need",
		"i always", "i never", "my preference",
	}
	for _, n := range prefNeedles {
		if strings.Contains(lower, n) {
			return "preference", true
		}
	}

	projectNeedles := []string{
		"we're working on", "the project", "our team",
		"the deadline", "the sprint", "we decided",
		"we're building", "the codebase", "we shipped",
	}
	for _, n := range projectNeedles {
		if strings.Contains(lower, n) {
			return "project", true
		}
	}

	rememberNeedles := []string{
		"remember that", "remember this", "don't forget",
		"keep in mind", "note that", "for future",
	}
	for _, n := range rememberNeedles {
		if strings.Contains(lower, n) {
			return "reference", true
		}
	}

	growthNeedles := []string{
		"i learned", "i realized", "i noticed",
		"i think", "going forward", "from now on",
	}
	for _, n := range growthNeedles {
		if strings.Contains(lower, n) {
			return "growth", true
		}
	}

	return "", false
}

func deriveTags(text, kind string) []string {
	tags := []string{kind}

	stopwords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true,
		"but": true, "in": true, "on": true, "at": true, "to": true,
		"for": true, "of": true, "with": true, "by": true, "from": true,
		"is": true, "it": true, "that": true, "this": true, "was": true,
		"are": true, "be": true, "has": true, "have": true, "had": true,
		"not": true, "what": true, "when": true, "where": true, "how": true,
		"all": true, "each": true, "every": true, "both": true, "few": true,
		"more": true, "most": true, "other": true, "some": true, "such": true,
		"than": true, "too": true, "very": true, "can": true, "will": true,
		"just": true, "don't": true, "should": true, "now": true, "i": true,
		"we": true, "they": true, "my": true, "our": true, "your": true,
	}

	words := strings.Fields(strings.ToLower(text))
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?\"'()-")
		if len(w) > 3 && !stopwords[w] && len(tags) < 8 {
			tags = append(tags, w)
		}
	}
	return tags
}

// PromptContext retrieves relevant memories for prompt injection.
func PromptContext(s *Store, query, cwd string) (string, error) {
	mode := CurrentMode()
	switch mode {
	case ModeOff:
		return "(Memory recall is disabled: memory_mode=off.)", nil
	case ModeDryRun:
		return "(Memory recall is disabled for this run: memory_mode=dry-run captures events without using them in replies.)", nil
	}

	cwd = normalizeProjectPath(cwd)

	// Generate query embedding if available.
	var queryEmb []float32
	if EmbedFunc != nil {
		emb, err := EmbedFunc(query)
		if err != nil {
			log.Printf("memory: query embedding error (non-fatal): %v", err)
		} else {
			queryEmb = emb
		}
	}

	results, err := s.Search(query, queryEmb, cwd, 5)
	if err != nil {
		return "", fmt.Errorf("memory: search: %w", err)
	}

	projects, err := s.KnownProjects()
	if err != nil {
		return "", fmt.Errorf("memory: list projects: %w", err)
	}
	referencedProjects := matchedProjectsForQuery(query, cwd, projects)

	var b strings.Builder
	if len(results) > 0 {
		b.WriteString("Recalled memories:\n")
		writePromptResults(&b, results)
	}

	for _, project := range referencedProjects {
		crossResults, err := s.Search(query, queryEmb, project, 3)
		if err != nil {
			return "", fmt.Errorf("memory: cross-project search: %w", err)
		}

		var filtered []Result
		for _, r := range crossResults {
			if r.Scope == "project" && normalizeProjectPath(r.Project) == project {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) == 0 {
			continue
		}

		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		name := filepath.Base(project)
		if name == "" || name == "." {
			name = project
		}
		b.WriteString(fmt.Sprintf("Cross-project context (from %s — treat as reference, not current project rules):\n", name))
		writePromptResults(&b, filtered)
	}

	if b.Len() == 0 {
		return "(No relevant memories recalled.)", nil
	}
	return strings.TrimSpace(b.String()), nil
}

func writePromptResults(b *strings.Builder, results []Result) {
	for _, r := range results {
		if len(r.Tags) > 0 {
			b.WriteString(fmt.Sprintf("- [%s] %s\n", strings.Join(r.Tags, ", "), r.Text))
		} else {
			b.WriteString("- " + r.Text + "\n")
		}
	}
}

func matchedProjectsForQuery(query, cwd string, projects []string) []string {
	query = normalizeProjectReference(query)
	cwd = normalizeProjectPath(cwd)

	seen := map[string]bool{}
	var matched []string
	for _, project := range projects {
		project = normalizeProjectPath(project)
		if project == "" || project == cwd || seen[project] {
			continue
		}
		if projectMentioned(query, filepath.Base(project)) {
			seen[project] = true
			matched = append(matched, project)
		}
	}
	return matched
}

func projectMentioned(query, name string) bool {
	query = normalizeProjectReference(query)
	name = normalizeProjectReference(name)
	if query == "" || name == "" {
		return false
	}
	if strings.Contains(query, name) {
		return true
	}
	compactQuery := strings.ReplaceAll(query, " ", "")
	compactName := strings.ReplaceAll(name, " ", "")
	return compactName != "" && strings.Contains(compactQuery, compactName)
}

func normalizeProjectReference(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// MemoryModeFile is a helper for the system prompt to read memory_mode.
func MemoryModeFile() string {
	cfg := struct {
		MemoryMode string `json:"memory_mode"`
	}{}
	if err := store.ReadJSON("config.json", &cfg); err != nil {
		return "off"
	}
	if cfg.MemoryMode == "" {
		return "off"
	}
	return cfg.MemoryMode
}

// SetMode writes memory_mode to the config file.
func SetMode(mode Mode) error {
	configMu.Lock()
	defer configMu.Unlock()

	path := store.ConfigFile("config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	modeBytes, _ := json.Marshal(string(mode))
	raw["memory_mode"] = modeBytes
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, out, 0o600)
}
