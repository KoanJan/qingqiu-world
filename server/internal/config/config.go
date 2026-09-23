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
//   - DATA_ROOT: Root directory for all data storage (default: ../data, relative to executable)
//   - PORT: Server listen port (default: 8000)
//   - SUMMARY_WINDOW_SIZE: Number of messages before triggering summary generation (default: 5)
//   - SUMMARY_TOKEN_THRESHOLD: Token budget threshold for fallback summary trigger (default: 32000)
//   - LOG_LEVEL: Logging level (default: INFO)
//   - FOCUSED_WORK_MAX_ITERATIONS: Maximum iterations for FocusedLoop (default: 300)
//   - AOS_ROOT: Root directory for agent-owned resources (default: DATA_ROOT/aos)
//   - AOS_META_ROOT: Root directory for runtime-owned agent metadata (default: DATA_ROOT/aosmeta)
//   - JINSHU_ROOT: Root directory for person-level jinshu (锦书) files (default: DATA_ROOT/jinshu)
//   - CONTEXT_WINDOW_ITERATIONS: Number of recent iterations visible to agent (default: 10)
//   - NOTES_MAX_CHARS: Maximum character limit for agent notes (default: 5000)
//   - KB_FLAT_THRESHOLD: Minimum vector count before HNSW building (default: 1000)
package config

import (
	"os"
	"path/filepath"
	"strconv"
)

// AppVersion is the current application version.
const AppVersion = "0.1.15"

// globalSettings is the singleton configuration instance.
var globalSettings *Settings

// Settings holds all application configuration values.
type Settings struct {
	DataRoot                 string // Root directory for all data storage
	LogDir                   string // Directory for log files (default: logs/)
	SummaryWindowSize        int    // Number of messages before triggering summary generation
	SummaryTokenThreshold    int    // Token budget threshold for fallback summary trigger
	LogLevel                 string // Logging level (DEBUG, INFO, WARN, ERROR)
	FocusedWorkMaxIterations int    // Maximum iterations for FocusedLoop
	AOSRoot                  string // Root directory for agent-owned resources
	AOSMetaRoot              string // Root directory for runtime-owned agent metadata
	JinshuRoot               string // Root directory for person-level jinshu (锦书) files
	MinIterationWindow       int    // Minimum iterations visible to agent (anchor size)
	MaxIterationWindow       int    // Maximum iterations before bulk-shrink triggers
	NotesMaxChars            int    // Maximum character limit for agent notes
	KBFlatThreshold          int    // Minimum vector count before building an HNSW index
}

// Init loads configuration from environment variables with defaults.
func Init() {
	dataRoot := expandHome(getEnv("DATA_ROOT", filepath.Join("..", "data")))

	globalSettings = &Settings{
		DataRoot:                 dataRoot,
		LogDir:                   expandHome(getEnv("LOG_DIR", "logs")),
		SummaryWindowSize:        getEnvInt("SUMMARY_WINDOW_SIZE", 50),
		SummaryTokenThreshold:    getEnvInt("SUMMARY_TOKEN_THRESHOLD", 16000),
		LogLevel:                 getEnv("LOG_LEVEL", "INFO"),
		FocusedWorkMaxIterations: getEnvInt("FOCUSED_WORK_MAX_ITERATIONS", 300),
		AOSRoot:                  expandHome(getEnv("AOS_ROOT", "")),
		AOSMetaRoot:              expandHome(getEnv("AOS_META_ROOT", "")),
		JinshuRoot:               expandHome(getEnv("JINSHU_ROOT", "")),
		MinIterationWindow:       getEnvInt("MIN_ITERATION_WINDOW", 10),
		MaxIterationWindow:       getEnvInt("MAX_ITERATION_WINDOW", 100),
		NotesMaxChars:            getEnvInt("NOTES_MAX_CHARS", 10000),
		KBFlatThreshold:          getPositiveEnvInt("KB_FLAT_THRESHOLD", 1000),
	}
}

// GetAOSRoot returns the root directory that contains agent-owned resources.
func (s *Settings) GetAOSRoot() string {
	if s.AOSRoot != "" {
		return s.AOSRoot
	}
	return filepath.Join(s.DataRoot, "aos")
}

// GetAOSMetaRoot returns the root directory for runtime-owned agent metadata.
func (s *Settings) GetAOSMetaRoot() string {
	if s.AOSMetaRoot != "" {
		return s.AOSMetaRoot
	}
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
// Falls back to DATA_ROOT/jinshu if JINSHU_ROOT is not explicitly set.
// Jinshu is an independent person-level feature, so its files are stored at the
// same level as workspace and private-space — not under the workspace root.
func (s *Settings) GetJinshuRoot() string {
	if s.JinshuRoot != "" {
		return s.JinshuRoot
	}
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

// getEnvInt returns the environment variable value as an integer or the fallback if not set/invalid.
func getEnvInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			return n
		}
	}
	return fallback
}

// getPositiveEnvInt reads a strictly positive integer or returns fallback.
// Index thresholds must never silently become zero, which would force HNSW
// builds for every small knowledge base.
func getPositiveEnvInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			return n
		}
	}
	return fallback
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
