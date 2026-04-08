package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/1broseidon/moxie/internal/store"
)

func setupTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	restore := store.SetConfigDir(dir)
	t.Cleanup(restore)

	s, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// writeTestConfig writes a minimal config.json in the test config dir.
func writeTestConfig(t *testing.T, mode string) {
	t.Helper()
	cfgPath := store.ConfigFile("config.json")
	os.MkdirAll(filepath.Dir(cfgPath), 0o700)
	os.WriteFile(cfgPath, []byte(`{"token":"x","chat_id":1,"memory_mode":"`+mode+`"}`), 0o600)
}

func TestOpenAndMigrate(t *testing.T) {
	s := setupTestStore(t)
	n, err := s.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}
}

func TestAddAndSearch(t *testing.T) {
	s := setupTestStore(t)

	facts := []Fact{
		{Text: "I prefer minimalist systems over opaque databases", Kind: "preference", Tags: []string{"preference", "minimalist", "systems"}},
		{Text: "We are building Moxie a chat agent service", Kind: "project", Tags: []string{"project", "moxie", "chat"}},
		{Text: "The deadline for the alpha release is March 15", Kind: "project", Tags: []string{"project", "deadline", "alpha"}},
		{Text: "I learned that append-only logs are safer than mutable state", Kind: "growth", Tags: []string{"growth", "append-only", "logs"}},
	}

	for _, f := range facts {
		if _, err := s.Add(f); err != nil {
			t.Fatalf("Add(%q): %v", f.Text, err)
		}
	}

	n, _ := s.Count()
	if n != 4 {
		t.Fatalf("expected 4, got %d", n)
	}

	// FTS5-only search (no embedding).
	results, err := s.Search("minimalist systems", nil, "", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'minimalist systems'")
	}
	if results[0].Text != "I prefer minimalist systems over opaque databases" {
		t.Fatalf("expected first result to be preference, got %q", results[0].Text)
	}

	// Search for project-related content.
	results, err = s.Search("Moxie chat agent", nil, "", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'Moxie chat agent'")
	}

	// Search for deadline.
	results, err = s.Search("alpha release deadline", nil, "", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'alpha release deadline'")
	}
}

func TestProjectScopedAdd(t *testing.T) {
	s := setupTestStore(t)

	globalID, err := s.Add(Fact{Text: "I prefer terse replies", Kind: "preference"})
	if err != nil {
		t.Fatalf("Add global: %v", err)
	}

	var scope, project string
	if err := s.db.QueryRow(`SELECT scope, project FROM memories WHERE id = ?`, globalID).Scan(&scope, &project); err != nil {
		t.Fatalf("select global memory: %v", err)
	}
	if scope != "global" || project != "" {
		t.Fatalf("expected global memory, got scope=%q project=%q", scope, project)
	}

	projectPath := filepath.Join(t.TempDir(), "alpha")
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	projectID, err := s.Add(Fact{Text: "The auth service lives here", Kind: "project", Scope: "project", Project: projectPath})
	if err != nil {
		t.Fatalf("Add project: %v", err)
	}

	if err := s.db.QueryRow(`SELECT scope, project FROM memories WHERE id = ?`, projectID).Scan(&scope, &project); err != nil {
		t.Fatalf("select project memory: %v", err)
	}
	if scope != "project" || project != filepath.Clean(projectPath) {
		t.Fatalf("expected project memory, got scope=%q project=%q", scope, project)
	}
}

func TestProjectScopedSearch(t *testing.T) {
	s := setupTestStore(t)

	base := t.TempDir()
	projectA := filepath.Join(base, "alpha")
	projectB := filepath.Join(base, "beta")
	os.MkdirAll(projectA, 0o700)
	os.MkdirAll(projectB, 0o700)

	if _, err := s.Add(Fact{Text: "The deploy pipeline uses GitHub Actions", Kind: "project", Scope: "project", Project: projectA}); err != nil {
		t.Fatalf("Add alpha: %v", err)
	}
	if _, err := s.Add(Fact{Text: "The deploy pipeline uses Buildkite", Kind: "project", Scope: "project", Project: projectB}); err != nil {
		t.Fatalf("Add beta: %v", err)
	}
	if _, err := s.Add(Fact{Text: "I prefer terse replies", Kind: "preference"}); err != nil {
		t.Fatalf("Add global: %v", err)
	}

	resultsA, err := s.Search("deploy pipeline", nil, projectA, 5)
	if err != nil {
		t.Fatalf("Search alpha: %v", err)
	}
	if len(resultsA) == 0 {
		t.Fatal("expected alpha-scoped results")
	}
	foundA := false
	for _, r := range resultsA {
		if r.Project == filepath.Clean(projectB) {
			t.Fatalf("beta memory leaked into alpha search: %+v", r)
		}
		if r.Project == filepath.Clean(projectA) {
			foundA = true
		}
	}
	if !foundA {
		t.Fatal("expected alpha project memory in alpha search")
	}

	resultsB, err := s.Search("deploy pipeline", nil, projectB, 5)
	if err != nil {
		t.Fatalf("Search beta: %v", err)
	}
	if len(resultsB) == 0 {
		t.Fatal("expected beta-scoped results")
	}
	foundB := false
	for _, r := range resultsB {
		if r.Project == filepath.Clean(projectA) {
			t.Fatalf("alpha memory leaked into beta search: %+v", r)
		}
		if r.Project == filepath.Clean(projectB) {
			foundB = true
		}
	}
	if !foundB {
		t.Fatal("expected beta project memory in beta search")
	}
}

func TestIsDuplicate(t *testing.T) {
	s := setupTestStore(t)

	s.Add(Fact{Text: "I prefer Go over Python", Kind: "preference"})

	dup, err := s.IsDuplicate("I prefer Go over Python", "")
	if err != nil {
		t.Fatal(err)
	}
	if !dup {
		t.Fatal("expected duplicate")
	}

	// Whitespace variation should also be duplicate.
	dup, _ = s.IsDuplicate("i  prefer  go  over  python", "")
	if !dup {
		t.Fatal("expected whitespace-normalized duplicate")
	}

	// Different text should not be duplicate.
	dup, _ = s.IsDuplicate("I prefer Rust over C++", "")
	if dup {
		t.Fatal("expected not duplicate")
	}
}

func TestIsDuplicateCrossProject(t *testing.T) {
	s := setupTestStore(t)

	base := t.TempDir()
	projectA := filepath.Join(base, "alpha")
	projectB := filepath.Join(base, "beta")
	os.MkdirAll(projectA, 0o700)
	os.MkdirAll(projectB, 0o700)

	text := "The release checklist lives in docs/release.md"
	if _, err := s.Add(Fact{Text: text, Kind: "reference", Scope: "project", Project: projectA}); err != nil {
		t.Fatalf("Add project memory: %v", err)
	}

	dup, err := s.IsDuplicate(text, projectB)
	if err != nil {
		t.Fatalf("IsDuplicate cross-project: %v", err)
	}
	if dup {
		t.Fatal("project memory should not duplicate across different projects")
	}

	dup, err = s.IsDuplicate(text, projectA)
	if err != nil {
		t.Fatalf("IsDuplicate same-project: %v", err)
	}
	if !dup {
		t.Fatal("project memory should duplicate within the same project")
	}

	if _, err := s.Add(Fact{Text: "I prefer Go over Python", Kind: "preference"}); err != nil {
		t.Fatalf("Add global memory: %v", err)
	}
	dup, err = s.IsDuplicate("I prefer Go over Python", projectB)
	if err != nil {
		t.Fatalf("IsDuplicate global: %v", err)
	}
	if !dup {
		t.Fatal("global memory should duplicate across projects")
	}
}

func TestCaptureModeBehavior(t *testing.T) {
	s := setupTestStore(t)
	writeTestConfig(t, "off")

	job := store.PendingJob{
		ID:     "test-1",
		Source: "telegram",
		Prompt: "I prefer transparent systems over opaque automation",
		State:  store.State{Backend: "claude"},
	}

	if err := Capture(s, job); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	n, _ := s.Count()
	if n != 0 {
		t.Fatalf("mode=off should not capture, got %d", n)
	}

	// Switch to dry-run.
	writeTestConfig(t, "dry-run")

	if err := Capture(s, job); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	n, _ = s.Count()
	if n != 1 {
		t.Fatalf("mode=dry-run should capture, got %d", n)
	}

	// PromptContext should not return memories in dry-run.
	ctx, err := PromptContext(s, "transparent systems", "")
	if err != nil {
		t.Fatalf("PromptContext: %v", err)
	}
	if ctx != "(Memory recall is disabled for this run: memory_mode=dry-run captures events without using them in replies.)" {
		t.Fatalf("unexpected dry-run context: %s", ctx)
	}

	// Switch to on.
	writeTestConfig(t, "on")

	ctx, err = PromptContext(s, "transparent systems", "")
	if err != nil {
		t.Fatalf("PromptContext: %v", err)
	}
	if ctx == "(Memory recall is disabled" || ctx == "(No relevant memories recalled.)" {
		t.Fatalf("mode=on should return recalled memories, got: %s", ctx)
	}
}

func TestCaptureSkipsUnsupportedSources(t *testing.T) {
	s := setupTestStore(t)
	writeTestConfig(t, "on")

	// Subagent source should be skipped.
	job := store.PendingJob{
		ID:     "sub-1",
		Source: "subagent-synthesis",
		Prompt: "I prefer fast builds",
		State:  store.State{Backend: "claude"},
	}
	Capture(s, job)
	n, _ := s.Count()
	if n != 0 {
		t.Fatal("subagent source should be skipped")
	}

	// Delegated task should be skipped.
	job2 := store.PendingJob{
		ID:            "del-1",
		Source:        "telegram",
		Prompt:        "I prefer fast builds",
		DelegatedTask: "some task",
		ParentJobID:   "parent-1",
		State:         store.State{Backend: "claude"},
	}
	Capture(s, job2)
	n, _ = s.Count()
	if n != 0 {
		t.Fatal("delegated task should be skipped")
	}

	// Direct telegram should work.
	job3 := store.PendingJob{
		ID:     "tg-1",
		Source: "telegram",
		Prompt: "I prefer fast builds and minimal dependencies",
		State:  store.State{Backend: "claude"},
	}
	Capture(s, job3)
	n, _ = s.Count()
	if n != 1 {
		t.Fatalf("telegram source should capture, got %d", n)
	}
}

func TestCaptureDeduplicates(t *testing.T) {
	s := setupTestStore(t)
	writeTestConfig(t, "on")

	job := store.PendingJob{
		ID:     "dup-1",
		Source: "telegram",
		Prompt: "I prefer SQLite over Postgres for embedded use",
		State:  store.State{Backend: "claude"},
	}

	Capture(s, job)
	Capture(s, job)
	Capture(s, job)

	n, _ := s.Count()
	if n != 1 {
		t.Fatalf("expected 1 after dedup, got %d", n)
	}
}

func TestHybridSearchRRF(t *testing.T) {
	s := setupTestStore(t)

	// Add facts with embeddings to test vector path.
	// Using trivial embeddings for testing (not real semantic vectors).
	emb1 := make([]float32, EmbedDim)
	emb1[0] = 1.0
	emb2 := make([]float32, EmbedDim)
	emb2[1] = 1.0
	emb3 := make([]float32, EmbedDim)
	emb3[0] = 0.9
	emb3[1] = 0.1

	s.Add(Fact{Text: "The deploy pipeline uses GitHub Actions", Kind: "project", Tags: []string{"deploy", "github"}, Embedding: emb1})
	s.Add(Fact{Text: "I prefer automated testing over manual QA", Kind: "preference", Tags: []string{"testing", "automated"}, Embedding: emb2})
	s.Add(Fact{Text: "Our CI runs on every push to main", Kind: "project", Tags: []string{"ci", "main"}, Embedding: emb3})

	// Search with both text and embedding (close to emb1).
	queryEmb := make([]float32, EmbedDim)
	queryEmb[0] = 0.95
	queryEmb[1] = 0.05

	results, err := s.Search("deploy pipeline", queryEmb, "", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	// The deploy pipeline fact should rank highest (matches both FTS and vector).
	if results[0].Text != "The deploy pipeline uses GitHub Actions" {
		t.Fatalf("expected deploy pipeline first, got %q", results[0].Text)
	}
}

func TestScopeClassification(t *testing.T) {
	tests := []struct {
		kind string
		want string
	}{
		{"preference", "global"},
		{"growth", "global"},
		{"project", "project"},
		{"reference", "project"},
		{"unknown", "global"},
	}
	for _, tt := range tests {
		if got := scopeForKind(tt.kind); got != tt.want {
			t.Errorf("scopeForKind(%q) = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestCrossProjectAdvisoryRecall(t *testing.T) {
	s := setupTestStore(t)
	writeTestConfig(t, "on")

	base := t.TempDir()
	projectAlpha := filepath.Join(base, "alpha")
	projectBeta := filepath.Join(base, "beta")
	os.MkdirAll(projectAlpha, 0o700)
	os.MkdirAll(projectBeta, 0o700)

	if _, err := s.Add(Fact{Text: "The deploy pipeline uses GitHub Actions", Kind: "project", Scope: "project", Project: projectAlpha, Tags: []string{"project", "deploy"}}); err != nil {
		t.Fatalf("Add alpha memory: %v", err)
	}

	ctx, err := PromptContext(s, "What's the deploy pipeline?", projectBeta)
	if err != nil {
		t.Fatalf("PromptContext without project reference: %v", err)
	}
	if strings.Contains(ctx, "GitHub Actions") {
		t.Fatalf("unexpected cross-project leak without project reference: %s", ctx)
	}

	ctx, err = PromptContext(s, "For alpha, what's the deploy pipeline?", projectBeta)
	if err != nil {
		t.Fatalf("PromptContext with project reference: %v", err)
	}
	wantHeader := "Cross-project context (from alpha — treat as reference, not current project rules):"
	if !strings.Contains(ctx, wantHeader) {
		t.Fatalf("expected advisory header %q in context: %s", wantHeader, ctx)
	}
	if !strings.Contains(ctx, "The deploy pipeline uses GitHub Actions") {
		t.Fatalf("expected alpha memory in cross-project context: %s", ctx)
	}
}

func TestBuildFTSQuery(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"minimalist systems", "minimalist* OR systems*"},
		{"deploy", "deploy*"},
		{"a b", ""}, // too short after trim
		{"", ""},
	}
	for _, tt := range tests {
		got := buildFTSQuery(tt.input)
		if tt.expected == "" && got == tt.input {
			continue
		}
		if got != tt.expected {
			t.Errorf("buildFTSQuery(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestClassifySegment(t *testing.T) {
	tests := []struct {
		seg   string
		kind  string
		match bool
	}{
		{"I prefer minimalist systems", "preference", true},
		{"I don't like verbose logging", "preference", true},
		{"We're working on the auth rewrite", "project", true},
		{"Remember that the API key rotates weekly", "reference", true},
		{"I learned that Go is great for CLIs", "growth", true},
		{"Hello how are you today", "", false},
		{"What time is it right now", "", false},
	}
	for _, tt := range tests {
		kind, ok := classifySegment(tt.seg)
		if ok != tt.match {
			t.Errorf("classifySegment(%q): match=%v, want %v", tt.seg, ok, tt.match)
		}
		if ok && kind != tt.kind {
			t.Errorf("classifySegment(%q): kind=%q, want %q", tt.seg, kind, tt.kind)
		}
	}
}
