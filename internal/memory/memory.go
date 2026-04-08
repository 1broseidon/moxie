// Package memory provides persistent, searchable memory for Moxie using
// SQLite with FTS5 (keyword/BM25) and sqlite-vec (vector/cosine similarity).
// Retrieval merges both strategies via Reciprocal Rank Fusion (RRF).
package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"

	"github.com/1broseidon/moxie/internal/store"
)

func init() { sqlite_vec.Auto() }

// Mode controls memory behavior.
type Mode string

const (
	ModeOff    Mode = "off"
	ModeDryRun Mode = "dry-run"
	ModeOn     Mode = "on"
)

// EmbedDim is the embedding vector dimensionality.
// all-MiniLM-L6-v2 = 384 (via hugot pure Go backend).
// Cannot change after DB creation without rebuild.
const EmbedDim = 384

// RRF constant — controls how much weight goes to top-ranked results.
const rrfK = 60

// Store is the SQLite-backed memory store. Safe for concurrent use.
type Store struct {
	mu sync.Mutex
	db *sql.DB
}

// Fact is an extracted memory fact ready for storage.
type Fact struct {
	Text      string    `json:"text"`
	Kind      string    `json:"kind"` // preference, project, growth, reference
	Tags      []string  `json:"tags,omitempty"`
	Source    string    `json:"source,omitempty"` // telegram, slack, webex
	JobID     string    `json:"job_id,omitempty"`
	ThreadID  string    `json:"thread_id,omitempty"`
	Backend   string    `json:"backend,omitempty"`
	Scope     string    `json:"scope,omitempty"`   // global or project
	Project   string    `json:"project,omitempty"` // cwd path, empty for global
	Embedding []float32 `json:"embedding,omitempty"`
}

// Result is a scored memory returned by search.
type Result struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Kind      string    `json:"kind"`
	Tags      []string  `json:"tags,omitempty"`
	Scope     string    `json:"scope"`
	Project   string    `json:"project"`
	Score     float64   `json:"score"`
	CreatedAt time.Time `json:"created_at"`
}

// DBPath returns the path to the memory database file.
func DBPath() string {
	return store.ConfigFile("memory.db")
}

// Open opens (or creates) the memory database.
func Open() (*Store, error) {
	path := DBPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("memory: create dir: %w", err)
	}

	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("memory: open db: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("memory: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS memories (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			text       TEXT    NOT NULL,
			kind       TEXT    NOT NULL DEFAULT 'preference',
			tags       TEXT    DEFAULT '[]',
			source     TEXT    DEFAULT '',
			job_id     TEXT    DEFAULT '',
			thread_id  TEXT    DEFAULT '',
			backend    TEXT    DEFAULT '',
			scope      TEXT    NOT NULL DEFAULT 'global',
			project    TEXT    NOT NULL DEFAULT '',
			created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		);
	`)
	if err != nil {
		return fmt.Errorf("create tables: %w", err)
	}

	if err := ensureColumn(db, "memories", "scope", `ALTER TABLE memories ADD COLUMN scope TEXT NOT NULL DEFAULT 'global'`); err != nil {
		return fmt.Errorf("add scope column: %w", err)
	}
	if err := ensureColumn(db, "memories", "project", `ALTER TABLE memories ADD COLUMN project TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add project column: %w", err)
	}

	ftsExists, err := tableExists(db, "memory_fts")
	if err != nil {
		return fmt.Errorf("check memory_fts existence: %w", err)
	}
	rebuildFTS := !ftsExists
	if ftsExists {
		hasScope, err := tableHasColumn(db, "memory_fts", "scope")
		if err != nil {
			return fmt.Errorf("check memory_fts scope column: %w", err)
		}
		hasProject, err := tableHasColumn(db, "memory_fts", "project")
		if err != nil {
			return fmt.Errorf("check memory_fts project column: %w", err)
		}
		rebuildFTS = !hasScope || !hasProject
	}

	if rebuildFTS {
		if _, err := db.Exec(`DROP TABLE IF EXISTS memory_fts`); err != nil {
			return fmt.Errorf("drop memory_fts: %w", err)
		}
	}

	_, err = db.Exec(`
		DROP TRIGGER IF EXISTS memory_ai;
		DROP TRIGGER IF EXISTS memory_ad;
		DROP TRIGGER IF EXISTS memory_au;

		CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
			text, kind, tags, scope UNINDEXED, project UNINDEXED,
			content=memories,
			content_rowid=id,
			tokenize='porter unicode61'
		);

		-- Triggers to keep FTS5 index in sync with memories table.
		CREATE TRIGGER IF NOT EXISTS memory_ai AFTER INSERT ON memories BEGIN
			INSERT INTO memory_fts(rowid, text, kind, tags, scope, project)
			VALUES (new.id, new.text, new.kind, new.tags, new.scope, new.project);
		END;
		CREATE TRIGGER IF NOT EXISTS memory_ad AFTER DELETE ON memories BEGIN
			INSERT INTO memory_fts(memory_fts, rowid, text, kind, tags, scope, project)
			VALUES ('delete', old.id, old.text, old.kind, old.tags, old.scope, old.project);
		END;
		CREATE TRIGGER IF NOT EXISTS memory_au AFTER UPDATE ON memories BEGIN
			INSERT INTO memory_fts(memory_fts, rowid, text, kind, tags, scope, project)
			VALUES ('delete', old.id, old.text, old.kind, old.tags, old.scope, old.project);
			INSERT INTO memory_fts(rowid, text, kind, tags, scope, project)
			VALUES (new.id, new.text, new.kind, new.tags, new.scope, new.project);
		END;
	`)
	if err != nil {
		return fmt.Errorf("create fts schema: %w", err)
	}

	if rebuildFTS {
		if _, err := db.Exec(`INSERT INTO memory_fts(memory_fts) VALUES ('rebuild')`); err != nil {
			return fmt.Errorf("rebuild fts: %w", err)
		}
	}

	// Create vec0 table for vector search.
	_, err = db.Exec(fmt.Sprintf(
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_vec USING vec0(embedding float[%d])`, EmbedDim))
	if err != nil {
		return fmt.Errorf("create vec0: %w", err)
	}
	return nil
}

func ensureColumn(db *sql.DB, table, column, alterStmt string) error {
	exists, err := tableHasColumn(db, table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = db.Exec(alterStmt)
	return err
}

func tableExists(db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func tableHasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Add inserts a fact into the memory store.
// If the fact has an embedding, it is stored in the vector index too.
func (s *Store) Add(f Fact) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tagsJSON, _ := json.Marshal(f.Tags)
	scope, project := normalizeFactScope(f)

	res, err := s.db.Exec(
		`INSERT INTO memories (text, kind, tags, source, job_id, thread_id, backend, scope, project) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.Text, f.Kind, string(tagsJSON), f.Source, f.JobID, f.ThreadID, f.Backend, scope, project,
	)
	if err != nil {
		return 0, fmt.Errorf("memory: insert: %w", err)
	}
	id, _ := res.LastInsertId()

	if len(f.Embedding) == EmbedDim {
		blob, err := sqlite_vec.SerializeFloat32(f.Embedding)
		if err != nil {
			return id, fmt.Errorf("memory: serialize embedding: %w", err)
		}
		if _, err := s.db.Exec(
			`INSERT INTO memory_vec (rowid, embedding) VALUES (?, ?)`, id, blob,
		); err != nil {
			return id, fmt.Errorf("memory: insert vec: %w", err)
		}
	}

	return id, nil
}

func normalizeFactScope(f Fact) (string, string) {
	scope := strings.TrimSpace(f.Scope)
	if scope == "" {
		scope = "global"
	}
	project := normalizeProjectPath(f.Project)
	if scope != "project" {
		project = ""
	}
	return scope, project
}

func normalizeProjectPath(project string) string {
	project = strings.TrimSpace(project)
	if project == "" {
		return ""
	}
	return filepath.Clean(project)
}

// IsDuplicate checks if an equivalent fact already exists within the current
// project scope. The same text may exist in different projects, but global
// memories duplicate across every project.
func (s *Store) IsDuplicate(text, project string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalized := normalizeText(text)
	project = normalizeProjectPath(project)

	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*)
		 FROM memories
		 WHERE LOWER(REPLACE(REPLACE(text, CHAR(10), ' '), '  ', ' ')) = ?
		   AND (project = ? OR scope = 'global')`,
		normalized, project,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// Search performs hybrid retrieval: FTS5 BM25 + vector cosine similarity,
// merged via Reciprocal Rank Fusion. Returns up to limit results.
func (s *Store) Search(query string, queryEmbedding []float32, project string, limit int) ([]Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 5
	}
	project = normalizeProjectPath(project)

	// Collect candidates from both strategies.
	ftsRanks := map[int64]int{}
	vecRanks := map[int64]int{}

	// 1. FTS5 keyword search with BM25 ranking.
	if query != "" {
		ftsQuery := buildFTSQuery(query)
		rows, err := s.db.Query(
			`SELECT memory_fts.rowid
			 FROM memory_fts
			 JOIN memories ON memories.id = memory_fts.rowid
			 WHERE memory_fts MATCH ?
			   AND (memories.scope = 'global' OR memories.project = ?)
			 ORDER BY bm25(memory_fts)
			 LIMIT ?`,
			ftsQuery, project, limit*3,
		)
		if err != nil {
			log.Printf("memory: fts search error (non-fatal): %v", err)
		} else {
			rank := 0
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err == nil {
					ftsRanks[id] = rank
					rank++
				}
			}
			rows.Close()
		}
	}

	// 2. Vector similarity search.
	if len(queryEmbedding) == EmbedDim {
		blob, err := sqlite_vec.SerializeFloat32(queryEmbedding)
		if err == nil {
			rows, err := s.db.Query(
				`SELECT candidates.rowid, candidates.distance
				 FROM (
				 	SELECT rowid, distance
				 	FROM memory_vec
				 	WHERE embedding MATCH ?
				 	LIMIT ?
				 ) AS candidates
				 JOIN memories ON memories.id = candidates.rowid
				 WHERE memories.scope = 'global' OR memories.project = ?
				 ORDER BY candidates.distance`,
				blob, limit*3, project,
			)
			if err != nil {
				log.Printf("memory: vec search error (non-fatal): %v", err)
			} else {
				rank := 0
				for rows.Next() {
					var id int64
					var dist float64
					if err := rows.Scan(&id, &dist); err == nil {
						vecRanks[id] = rank
						rank++
					}
				}
				rows.Close()
			}
		}
	}

	// 3. Reciprocal Rank Fusion.
	allIDs := map[int64]bool{}
	for id := range ftsRanks {
		allIDs[id] = true
	}
	for id := range vecRanks {
		allIDs[id] = true
	}

	if len(allIDs) == 0 {
		return nil, nil
	}

	type scored struct {
		id    int64
		score float64
	}
	var candidates []scored
	for id := range allIDs {
		score := 0.0
		if r, ok := ftsRanks[id]; ok {
			score += 1.0 / float64(rrfK+r)
		}
		if r, ok := vecRanks[id]; ok {
			score += 1.0 / float64(rrfK+r)
		}
		candidates = append(candidates, scored{id, score})
	}

	// Sort by score descending.
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[j].score > candidates[i].score {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	// 4. Hydrate results from the memories table.
	results := make([]Result, 0, len(candidates))
	for _, c := range candidates {
		var r Result
		var tagsJSON, createdAt string
		err := s.db.QueryRow(
			`SELECT id, text, kind, tags, scope, project, created_at FROM memories WHERE id = ?`, c.id,
		).Scan(&r.ID, &r.Text, &r.Kind, &tagsJSON, &r.Scope, &r.Project, &createdAt)
		if err != nil {
			continue
		}
		json.Unmarshal([]byte(tagsJSON), &r.Tags)
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		r.Score = c.score

		// Apply recency boost: exponential decay over 30 days.
		age := time.Since(r.CreatedAt)
		decayDays := 30.0
		recencyBoost := math.Exp(-age.Hours() / (24.0 * decayDays))
		r.Score += 0.5 * recencyBoost

		results = append(results, r)
	}

	// Re-sort after recency boost.
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[i].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	return results, nil
}

// KnownProjects returns all distinct project memory scopes with a non-empty
// project path.
func (s *Store) KnownProjects() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT DISTINCT project FROM memories WHERE scope = 'project' AND project != '' ORDER BY project`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []string
	for rows.Next() {
		var project string
		if err := rows.Scan(&project); err != nil {
			return nil, err
		}
		projects = append(projects, normalizeProjectPath(project))
	}
	return projects, rows.Err()
}

// Count returns the total number of stored memories.
func (s *Store) Count() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&n)
	return n, err
}

// MemoryStats holds aggregate counts for the stats command.
type MemoryStats struct {
	Total   int
	ByKind  map[string]int
	ByScope map[string]int
}

// Stats returns aggregate counts grouped by kind and scope.
func (s *Store) Stats() (MemoryStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var st MemoryStats
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&st.Total); err != nil {
		return st, err
	}

	st.ByKind = map[string]int{}
	rows, err := s.db.Query(`SELECT kind, COUNT(*) FROM memories GROUP BY kind`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err == nil {
			st.ByKind[kind] = count
		}
	}
	rows.Close()

	st.ByScope = map[string]int{}
	rows, err = s.db.Query(`SELECT scope, COUNT(*) FROM memories GROUP BY scope`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var scope string
		var count int
		if err := rows.Scan(&scope, &count); err == nil {
			st.ByScope[scope] = count
		}
	}
	rows.Close()

	return st, nil
}

// All returns every stored memory, ordered by creation time descending.
func (s *Store) All() ([]Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT id, text, kind, tags, scope, project, created_at FROM memories ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var r Result
		var tagsJSON, createdAt string
		if err := rows.Scan(&r.ID, &r.Text, &r.Kind, &tagsJSON, &r.Scope, &r.Project, &createdAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(tagsJSON), &r.Tags)
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		results = append(results, r)
	}
	return results, rows.Err()
}

// buildFTSQuery converts a natural language query into an FTS5 query.
// Each word becomes a prefix match joined with OR for broad recall.
func buildFTSQuery(query string) string {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return query
	}
	var parts []string
	for _, w := range words {
		w = sanitizeFTSWord(w)
		if len(w) < 2 {
			continue
		}
		parts = append(parts, w+"*")
	}
	if len(parts) == 0 {
		return query
	}
	return strings.Join(parts, " OR ")
}

func sanitizeFTSWord(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r):
			b.WriteRune(unicode.ToLower(r))
		case unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func normalizeText(s string) string {
	s = strings.ToLower(s)
	s = strings.Join(strings.Fields(s), " ")
	return s
}
