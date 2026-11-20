// Package config provides application-wide configuration management.
//
// Configuration is loaded from environment variables with sensible defaults.
// The global Settings singleton is initialized on first access via Get().
//
// Two deployment modes:
//   - Electron packaging: PORT and DATA_ROOT are injected by the main process;
//     .env file is not included in the bundle.
//   - Standalone server: .env file is required; DATA_ROOT defaults to ../data (project root).
//
// Environment variables:
//   - DATA_ROOT: Root directory for all data storage (default: ../data, relative to the working directory)
//   - LOG_DIR: Directory for log files (default: logs)
//   - PORT: Server listen port (default: 8000)
//   - SUMMARY_WINDOW_SIZE: Number of messages before triggering summary generation (default: 50)
//   - SUMMARY_TOKEN_THRESHOLD: Token budget threshold for fallback summary trigger (default: 16000)
//   - LOG_LEVEL: Logging level (default: INFO)
//   - FOCUSED_WORK_MAX_ITERATIONS: Maximum iterations for FocusedLoop (default: 300)
//   - DEV_DATA_MODE: Set to 1 only for managed developer data startup
//   - DEV_REPO_ROOT: Explicit Git checkout root for developer data identity
//   - DEV_BASELINE_SOURCE: Optional verified released-data directory for first-time developer baseline import
//   - MIN_ITERATION_WINDOW: Recent iterations visible to the agent (default: 10)
//   - MAX_ITERATION_WINDOW: Iterations before the context window is bulk-shrunk (default: 100)
//   - NOTES_MAX_CHARS: Maximum character limit for agent notes (default: 10000)
//   - KB_FLAT_THRESHOLD: Minimum vector count before HNSW building (default: 1000)
//   - KB_RETRIEVAL_MIN_TOKENS: Target lower bound for KB retrieval units (default: 64)
//   - KB_RETRIEVAL_MAX_TOKENS: Hard upper bound for KB retrieval units (default: 512)
//   - KB_CHUNK_OVERLAP_TOKENS: Overlap for splits within one oversized leaf (default: 64)
//   - KB_EMBEDDING_HARD_MAX_TOKENS: Embedding provider input guard (default: 8192)
//   - DAILY_MEMORY_REQUEST_INTERVAL_MS: Minimum interval between daily-memory shard opportunities (default: 5000; allowed range: 1000–60000)
//   - COGNITIVE_MAX_REQUESTS: Model-request limit for one background cognitive process (default: 12; minimum: 3)
//   - COGNITIVE_RECENT_REVIEW_HOURS: Age below which revisiting needs explicit confirmation (default: 24)
//   - COGNITIVE_ROUTE_MIN_SIMILARITY_PERCENT: Minimum description-to-daily similarity for a routed review, 1–100 (default: 35)
//   - RELEASE_MIGRATION_ONLY: Internal signal for running release migrations on a staged baseline only
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AppVersion is the latest released data version. Unreleased data changes use
// the separate development migration chain.
const AppVersion = "0.1.21"

// globalSettings is the singleton configuration instance.
var globalSettings *Settings

// Settings holds all application configuration values.
type Settings struct {
	DataRoot                     string // Root directory for all data storage
	LogDir                       string // Directory for log files (default: logs/)
	SummaryWindowSize            int    // Number of messages before triggering summary generation
	SummaryTokenThreshold        int    // Token budget threshold for fallback summary trigger
	LogLevel                     string // Logging level (DEBUG, INFO, WARN, ERROR)
	FocusedWorkMaxIterations     int    // Maximum iterations for FocusedLoop
	MinIterationWindow           int    // Minimum iterations visible to agent (anchor size)
	MaxIterationWindow           int    // Maximum iterations before bulk-shrink triggers
	NotesMaxChars                int    // Maximum character limit for agent notes
	KBFlatThreshold              int    // Minimum vector count before building an HNSW index
	KBRetrievalMinTokens         int    // Target lower bound for KB retrieval units
	KBRetrievalMaxTokens         int    // Hard upper bound for KB retrieval units
	KBChunkOverlapTokens         int    // Overlap for chunks split within one oversized leaf
	KBEmbeddingHardMaxTokens     int    // Embedding provider input guard for KB retrieval units
	DailyMemoryRequestIntervalMS int    // Minimum interval between daily-memory shard opportunities
	CognitiveMaxRequests         int    // Bounds one background cognitive process; runtime needs at least three requests
	CognitiveRecentReviewHours   int    // Requires explicit confirmation for a recent review
	CognitiveRouteMinSimilarity  int    // Minimum similarity percent for a system-routed review (1–100)
}

// Init loads configuration from environment variables with defaults.
func Init() {
	dataRoot := expandHome(getEnv("DATA_ROOT", filepath.Join("..", "data")))

	defaults := Settings{
		DataRoot:                     dataRoot,
		LogDir:                       expandHome(getEnv("LOG_DIR", "logs")),
		SummaryWindowSize:            50,
		SummaryTokenThreshold:        16000,
		LogLevel:                     getEnv("LOG_LEVEL", "INFO"),
		FocusedWorkMaxIterations:     300,
		MinIterationWindow:           10,
		MaxIterationWindow:           100,
		NotesMaxChars:                10000,
		KBFlatThreshold:              1000,
		KBRetrievalMinTokens:         64,
		KBRetrievalMaxTokens:         512,
		KBChunkOverlapTokens:         64,
		KBEmbeddingHardMaxTokens:     8192,
		DailyMemoryRequestIntervalMS: 5000,
		CognitiveMaxRequests:         12,
		CognitiveRecentReviewHours:   24,
		CognitiveRouteMinSimilarity:  35,
	}
	s := defaults
	s.SummaryWindowSize = readInt("SUMMARY_WINDOW_SIZE", defaults.SummaryWindowSize, 1, 0)
	s.SummaryTokenThreshold = readInt("SUMMARY_TOKEN_THRESHOLD", defaults.SummaryTokenThreshold, 1, 0)
	s.FocusedWorkMaxIterations = readInt("FOCUSED_WORK_MAX_ITERATIONS", defaults.FocusedWorkMaxIterations, 1, 0)
	s.MinIterationWindow = readInt("MIN_ITERATION_WINDOW", defaults.MinIterationWindow, 1, 0)
	s.MaxIterationWindow = readInt("MAX_ITERATION_WINDOW", defaults.MaxIterationWindow, 1, 0)
	if s.MinIterationWindow >= s.MaxIterationWindow {
		warnConfig("MIN_ITERATION_WINDOW/MAX_ITERATION_WINDOW", "must satisfy 0 < min < max; using defaults")
		s.MinIterationWindow, s.MaxIterationWindow = defaults.MinIterationWindow, defaults.MaxIterationWindow
	}
	s.NotesMaxChars = readInt("NOTES_MAX_CHARS", defaults.NotesMaxChars, 1, 0)
	s.KBFlatThreshold = readInt("KB_FLAT_THRESHOLD", defaults.KBFlatThreshold, 1, 0)
	s.KBRetrievalMinTokens = readInt("KB_RETRIEVAL_MIN_TOKENS", defaults.KBRetrievalMinTokens, 1, 0)
	s.KBRetrievalMaxTokens = readInt("KB_RETRIEVAL_MAX_TOKENS", defaults.KBRetrievalMaxTokens, 1, 0)
	s.KBChunkOverlapTokens = readInt("KB_CHUNK_OVERLAP_TOKENS", defaults.KBChunkOverlapTokens, 0, 0)
	s.KBEmbeddingHardMaxTokens = readInt("KB_EMBEDDING_HARD_MAX_TOKENS", defaults.KBEmbeddingHardMaxTokens, 1, 0)
	if s.KBRetrievalMinTokens >= s.KBRetrievalMaxTokens || s.KBRetrievalMaxTokens > s.KBEmbeddingHardMaxTokens || s.KBChunkOverlapTokens >= s.KBRetrievalMaxTokens {
		warnConfig("KB_RETRIEVAL_MIN_TOKENS/KB_RETRIEVAL_MAX_TOKENS/KB_CHUNK_OVERLAP_TOKENS/KB_EMBEDDING_HARD_MAX_TOKENS", "inconsistent retrieval bounds; using defaults")
		s.KBRetrievalMinTokens, s.KBRetrievalMaxTokens = defaults.KBRetrievalMinTokens, defaults.KBRetrievalMaxTokens
		s.KBChunkOverlapTokens, s.KBEmbeddingHardMaxTokens = defaults.KBChunkOverlapTokens, defaults.KBEmbeddingHardMaxTokens
	}
	s.DailyMemoryRequestIntervalMS = readInt("DAILY_MEMORY_REQUEST_INTERVAL_MS", defaults.DailyMemoryRequestIntervalMS, 1000, 60000)
	s.CognitiveMaxRequests = readInt("COGNITIVE_MAX_REQUESTS", defaults.CognitiveMaxRequests, 3, 0)
	s.CognitiveRecentReviewHours = readInt("COGNITIVE_RECENT_REVIEW_HOURS", defaults.CognitiveRecentReviewHours, 1, 0)
	s.CognitiveRouteMinSimilarity = readInt("COGNITIVE_ROUTE_MIN_SIMILARITY_PERCENT", defaults.CognitiveRouteMinSimilarity, 1, 100)
	globalSettings = &s
}

// GetAOSRoot returns the root directory that contains agent-owned resources.
func (s *Settings) GetAOSRoot() string {
	return filepath.Join(s.DataRoot, "aos")
}

// GetAOSMetaRoot returns the root directory for runtime-owned agent metadata.
func (s *Settings) GetAOSMetaRoot() string {
	return filepath.Join(s.DataRoot, "aosmeta")
}

// Get returns the global Settings instance, initializing it if necessary.
func Get() *Settings {
	if globalSettings == nil {
		Init()
	}
	return globalSettings
}

// GetDataRoot returns the data root directory path.
func (s *Settings) GetDataRoot() string {
	return s.DataRoot
}

// DatabaseURL returns the SQLite database file path.
func (s *Settings) DatabaseURL() string {
	return filepath.Join(s.DataRoot, "db", "database.db")
}

// GetJinshuRoot returns the jinshu (锦书) root directory path.
// Jinshu is an independent person-level feature, so its files are stored at the
// same level as workspace and private-space — not under the workspace root.
func (s *Settings) GetJinshuRoot() string {
	return filepath.Join(s.DataRoot, "jinshu")
}

// GetAvatarsDir returns the directory path for agent avatar images.
func (s *Settings) GetAvatarsDir() string {
	return filepath.Join(s.DataRoot, "avatars")
}

// GetSettingsDir returns the directory for local user settings (data/settings/).
func (s *Settings) GetSettingsDir() string {
	return filepath.Join(s.DataRoot, "settings")
}

// GetBackgroundsDir returns the directory for user-uploaded background images (data/settings/backgrounds/).
func (s *Settings) GetBackgroundsDir() string {
	return filepath.Join(s.DataRoot, "settings", "backgrounds")
}

// GetKBDir returns the root directory path for knowledge base data.
// Each knowledge base has a subdirectory: {kb_dir}/{kb_id}/.
func (s *Settings) GetKBDir() string {
	return filepath.Join(s.DataRoot, "kb")
}

// getEnv returns the environment variable value or the fallback if not set.
func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

// readInt validates deployment values before any service starts. max=0 means
// there is no upper bound; empty values intentionally retain the default.
func readInt(key string, fallback, min, max int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < min || (max > 0 && n > max) {
		upper := "unbounded"
		if max > 0 {
			upper = strconv.Itoa(max)
		}
		warnConfig(key, fmt.Sprintf("invalid value %q; using default %d (allowed: %d..%s)", value, fallback, min, upper))
		return fallback
	}
	return n
}

func warnConfig(key, reason string) {
	fmt.Fprintf(os.Stderr, "Warning: configuration %s: %s\n", key, reason)
}

// homeDir returns the current user's home directory.
func homeDir() string {
	if dir, err := os.UserHomeDir(); err == nil {
		return dir
	}
	return os.Getenv("HOME")
}

// expandHome replaces a leading ~/ in the path with the user's home directory.
// Returns the path unchanged if it does not start with ~/.
func expandHome(path string) string {
	if len(path) >= 2 && path[:2] == "~/" {
		return filepath.Join(homeDir(), path[2:])
	}
	return path
}
