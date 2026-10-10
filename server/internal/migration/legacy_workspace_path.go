package migration

import (
	"path/filepath"
	"strconv"

	"qingqiu-world-server/internal/service/aos"
)

// legacySessionWorkspacePath identifies the pre-0.1.19 resource directory
// while importing or registering existing data. Runtime uses Workspace records.
func legacySessionWorkspacePath(personID, sessionID int64) string {
	return filepath.Join(legacySessionWorkspaceRoot(personID), strconv.FormatInt(sessionID, 10))
}

// legacySessionWorkspaceRoot locates the old Session-partitioned branch.
func legacySessionWorkspaceRoot(personID int64) string {
	return filepath.Join(aos.GetAgentOwnedSpacePath(personID), "work")
}

// legacySessionMetaDir identifies the metadata paired with an old Session
// directory while importing files. Runtime resolves it through Workspace.
func legacySessionMetaDir(personID, sessionID int64) string {
	return filepath.Join(aos.GetAgentMetaPath(personID), "work", strconv.FormatInt(sessionID, 10), ".meta")
}
