package workspace

import (
	"path/filepath"
	"strconv"

	"qingqiu-world-server/internal/config"
)

// GetSandboxPolicyDir returns the directory path for sandbox policy files
// for a given person and session, stored outside the agent-writable workspace
// to prevent tampering.
func GetSandboxPolicyDir(personID, sessionID int64) string {
	return filepath.Join(config.Get().GetDataRoot(), "aac",
		strconv.FormatInt(personID, 10), strconv.FormatInt(sessionID, 10))
}
