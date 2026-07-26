// Package workspace manages session workspace paths and directory lifecycle.
//
// This file handles AAC (agent access control) directory cleanup for sandbox
// policy files. AAC directories live under {DATA_ROOT}/aac/{personID}/{sessionID}/
// and are separate from workspace directories — they must be cleaned up independently
// when a session is deleted.
package workspace

import (
	"os"
	"path/filepath"
	"strconv"

	"qingqiu-world-server/internal/config"

	applogger "qingqiu-world-server/internal/logger"
)

// RemoveAac removes the AAC policy directory for the given session.
// This should be called alongside RemoveWorkspace when a session is deleted
// to prevent orphaned sandbox policy files from accumulating.
func RemoveAac(personID, sessionID int64) {
	aacDir := filepath.Join(config.Get().GetDataRoot(), "aac",
		strconv.FormatInt(personID, 10), strconv.FormatInt(sessionID, 10))
	if err := os.RemoveAll(aacDir); err != nil {
		applogger.Error("failed to remove AAC directory",
			"person_id", personID, "session_id", sessionID,
			"path", aacDir, "error", err)
	} else {
		applogger.Info("AAC directory removed",
			"person_id", personID, "session_id", sessionID,
			"path", aacDir)
	}
}
