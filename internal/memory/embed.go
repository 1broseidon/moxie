package memory

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
)

const (
	// defaultModel is the HuggingFace model ID for downloading.
	// all-MiniLM-L6-v2: 384-dim, ~90MB, fast on CPU, proven for semantic search.
	defaultModel = "KnightsAnalytics/all-MiniLM-L6-v2"
)

// Embedder wraps a hugot session and feature extraction pipeline.
type Embedder struct {
	mu       sync.Mutex
	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline
}

// modelCacheDir returns the directory where downloaded models are cached.
func modelCacheDir() string {
	dir := filepath.Join(os.Getenv("HOME"), ".cache", "moxie", "models")
	os.MkdirAll(dir, 0o700)
	return dir
}

// ensureModel downloads the model if not already cached. Returns the local path.
func ensureModel() (string, error) {
	cacheDir := modelCacheDir()
	modelPath := filepath.Join(cacheDir, "KnightsAnalytics_all-MiniLM-L6-v2")

	// Check if model already exists.
	if _, err := os.Stat(filepath.Join(modelPath, "model.onnx")); err == nil {
		return modelPath, nil
	}

	log.Printf("memory: downloading embedding model %s (one-time, ~90MB)...", defaultModel)
	downloadedPath, err := hugot.DownloadModel(defaultModel, cacheDir, hugot.NewDownloadOptions())
	if err != nil {
		return "", fmt.Errorf("download model: %w", err)
	}
	log.Printf("memory: model downloaded to %s", downloadedPath)
	return downloadedPath, nil
}

// NewEmbedder creates an embedder using the pure Go (GoMLX) backend.
// The model is downloaded on first use and cached locally.
func NewEmbedder() (*Embedder, error) {
	session, err := hugot.NewGoSession()
	if err != nil {
		return nil, fmt.Errorf("hugot session: %w", err)
	}

	modelPath, err := ensureModel()
	if err != nil {
		session.Destroy()
		return nil, fmt.Errorf("ensure model: %w", err)
	}

	config := hugot.FeatureExtractionConfig{
		ModelPath:    modelPath,
		Name:         "moxie-memory",
		OnnxFilename: "model.onnx",
	}

	pipeline, err := hugot.NewPipeline(session, config)
	if err != nil {
		session.Destroy()
		return nil, fmt.Errorf("hugot pipeline: %w", err)
	}

	return &Embedder{
		session:  session,
		pipeline: pipeline,
	}, nil
}

// Embed generates a 384-dimensional embedding for a single text string.
func (e *Embedder) Embed(text string) ([]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	result, err := e.pipeline.RunPipeline([]string{text})
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if len(result.Embeddings) == 0 || len(result.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("embed: empty result")
	}

	emb := make([]float32, len(result.Embeddings[0]))
	for i, v := range result.Embeddings[0] {
		emb[i] = float32(v)
	}
	return emb, nil
}

// Close releases the hugot session.
func (e *Embedder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != nil {
		return e.session.Destroy()
	}
	return nil
}

// InitEmbedder creates an Embedder and sets the package-level EmbedFunc.
// Returns the embedder (caller should defer Close) or nil on failure.
func InitEmbedder() *Embedder {
	emb, err := NewEmbedder()
	if err != nil {
		log.Printf("memory: embedder init failed (FTS5-only mode): %v", err)
		return nil
	}
	EmbedFunc = emb.Embed
	log.Printf("memory: embedder ready [model=%s dim=%d]", defaultModel, EmbedDim)
	return emb
}
