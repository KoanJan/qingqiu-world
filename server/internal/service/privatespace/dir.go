// Package privatespace implements the agent's private space — a persistent,
// fully self-directed directory space that belongs to an agent for life.
//
// Unlike workspace (which is session-scoped and system-structured), private-space
// has no preset directory layout — the agent organizes it however it wants.
// The system only provides the root path; everything inside is the agent's
// own responsibility.
package privatespace

import (
	"fmt"
	"os"
	"path/filepath"

	"qingqiu-world-server/internal/service/workspace"

	applogger "qingqiu-world-server/internal/logger"
)

// GetDirPath returns the full AOS root used by private-loop file tools.
// Directory is NOT created here — call InitDir to ensure it exists.
func GetDirPath(personID int64) string {
	return workspace.GetAgentOwnedSpacePath(personID)
}

// GetWorkDirPath returns the agent's working directory inside the private-space.
// The agent defaults to this directory, while file tools may use the complete
// AOS root returned by GetDirPath to access its other owned resources.
func GetWorkDirPath(personID int64) string {
	return workspace.GetPrivateSpacePath(personID)
}

// InitDir ensures the private-space directory tree exists, creating it lazily
// on first access. Returns both the root directory and working directory.
func InitDir(personID int64) (rootDir, workDir string, err error) {
	rootDir, workDir, _, err = workspace.InitPrivateSpace(personID)
	if err != nil {
		return "", "", fmt.Errorf("init private-space dir for person %d: %w", personID, err)
	}
	return rootDir, workDir, nil
}

// GetLogPath returns the path to the agent's activity log file.
func GetLogPath(personID int64) string {
	return filepath.Join(workspace.GetPrivateMetaDir(personID), "log.jsonl")
}

// RemoveDir removes the entire private-space directory for the given person.
// This should be called when an agent is deleted to prevent orphaned
// private-space files from accumulating.
func RemoveDir(personID int64) {
	resourcePath := GetWorkDirPath(personID)
	metaPath := workspace.GetPrivateMetaDir(personID)
	if err := os.RemoveAll(resourcePath); err != nil {
		applogger.Error("failed to remove private-space directory",
			"person_id", personID, "path", resourcePath, "error", err)
	}
	if err := os.RemoveAll(metaPath); err != nil {
		applogger.Error("failed to remove private-space metadata directory",
			"person_id", personID, "path", metaPath, "error", err)
	}
}
