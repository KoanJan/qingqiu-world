// Package workspace manages session workspace paths and directory lifecycle.
//
// The workspace layout is:
//
//	{workspaceRoot}/{person_id}/{session_id}/
//	  ├── .meta/       — system-managed files (notes.jsonl, fingerprint.txt)
//	  ├── output/      — agent's working output directory (read-write)
//	  └── received/    — files delivered from other persons (read-only copies)
//	      ├── delivery_1/
//	      ├── delivery_2/
//	      └── ...
//
// Using person_id instead of agent_id unifies the user and agent directory
// hierarchy under the Person model — no more special "0" value for users.
package workspace

import (
	"os"
	"path/filepath"
	"strconv"

	"qingqiu-world-server/internal/config"
)

// getRoot returns the workspace root directory resolved to an absolute path,
// so that paths shown to the agent in prompts and used as bash CWD are unambiguous.
func getRoot() string {
	root := config.Get().GetWorkspaceRoot()
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}
	return root
}

// GetWorkspacePath returns the workspace directory for a specific person and session.
// Path: {workspaceRoot}/{person_id}/{session_id}
func GetWorkspacePath(personID, sessionID int64) string {
	return filepath.Join(getRoot(), strconv.FormatInt(personID, 10), strconv.FormatInt(sessionID, 10))
}

// GetMetaDir returns the .meta directory path for system-managed files.
// Path: {workspaceRoot}/{person_id}/{session_id}/.meta
func GetMetaDir(personID, sessionID int64) string {
	return filepath.Join(GetWorkspacePath(personID, sessionID), ".meta")
}

// GetOutputDir returns the output directory path for agent working files.
// Path: {workspaceRoot}/{person_id}/{session_id}/output
func GetOutputDir(personID, sessionID int64) string {
	return filepath.Join(GetWorkspacePath(personID, sessionID), "output")
}

// GetReceivedDir returns the received directory path for files delivered
// by other persons.
// Path: {workspaceRoot}/{person_id}/{session_id}/received
func GetReceivedDir(personID, sessionID int64) string {
	return filepath.Join(GetWorkspacePath(personID, sessionID), "received")
}

// InitWorkspace creates the workspace directory structure for a session.
// Notes (notes.jsonl) are created on first write, not pre-initialized.
func InitWorkspace(personID, sessionID int64) string {
	ws := GetWorkspacePath(personID, sessionID)
	metaDir := filepath.Join(ws, ".meta")
	os.MkdirAll(metaDir, 0755)

	outputDir := GetOutputDir(personID, sessionID)
	os.MkdirAll(outputDir, 0755)

	receivedDir := GetReceivedDir(personID, sessionID)
	os.MkdirAll(receivedDir, 0755)

	return ws
}

// RemoveWorkspace removes the entire workspace directory for a session.
func RemoveWorkspace(personID, sessionID int64) {
	os.RemoveAll(GetWorkspacePath(personID, sessionID))
}
