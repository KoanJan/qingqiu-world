// Package privatespace implements the agent's private space — a persistent,
// fully self-directed directory space that belongs to an agent for life.
//
// Unlike workspace (which is task-scoped and system-structured), private-space
// has no preset directory layout — the agent organizes it however it wants.
// The system only provides the root path; everything inside is the agent's
// own responsibility.
package privatespace

import (
	"fmt"
	"os"
	"path/filepath"

	"qingqiu-world-server/internal/config"
)

// GetDirPath returns the private-space root directory path for the given person.
// Directory is NOT created here — call InitDir to ensure it exists.
func GetDirPath(personID int64) string {
	root := config.Get().GetPrivateSpaceRoot()
	return filepath.Join(root, fmt.Sprintf("%d", personID))
}

// GetWorkDirPath returns the agent's working directory inside the private-space.
// The agent operates within this subdirectory; file operations resolve relative
// paths against this directory. System files (like the log) live in the
// parent root directory, outside the agent's reach.
func GetWorkDirPath(personID int64) string {
	return filepath.Join(GetDirPath(personID), "space")
}

// InitDir ensures the private-space directory tree exists, creating it lazily
// on first access. Returns both the root directory and working directory.
func InitDir(personID int64) (rootDir, workDir string, err error) {
	rootDir = GetDirPath(personID)
	workDir = GetWorkDirPath(personID)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return "", "", fmt.Errorf("init private-space dir for person %d: %w", personID, err)
	}
	return rootDir, workDir, nil
}

// GetLogPath returns the path to the agent's activity log file.
func GetLogPath(personID int64) string {
	return filepath.Join(GetDirPath(personID), "log.jsonl")
}
