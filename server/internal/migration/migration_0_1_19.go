package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	applogger "qingqiu-world-server/internal/logger"

	"gorm.io/gorm"
)

// 0.1.19 uses only historical columns, independent of later runtime models.
type legacyWork_0_1_19 struct {
	ID        int64
	SessionID int64
}

// migrate_0_1_19 registers real legacy directories without moving files or
// inventing an Action that created them. A Work is linked only when its former
// session directory exists; missing directories are reported as data loss.
func migrate_0_1_19(tx *gorm.DB) {
	var personIDs []int64
	if err := tx.Raw(`SELECT id FROM persons`).Scan(&personIDs).Error; err != nil {
		panic(fmt.Sprintf("0.1.19: list persons: %v", err))
	}
	for _, personID := range personIDs {
		root := legacySessionWorkspaceRoot(personID)
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			entries = nil
			err = nil
		}
		if err != nil {
			panic(fmt.Sprintf("0.1.19: inspect legacy work root %s: %v", root, err))
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			sessionID, err := strconv.ParseInt(entry.Name(), 10, 64)
			if err != nil || sessionID <= 0 {
				applogger.Warn("0.1.19: unrecognized legacy work directory", "person_id", personID, "name", entry.Name())
				continue
			}
			rel := filepath.Join("work", entry.Name())
			if err := tx.Exec(`INSERT INTO workspaces
				(person_id, relative_path, name, purpose, created_at) VALUES (?, ?, ?, '', ?)
				ON CONFLICT(person_id, relative_path) DO NOTHING`,
				personID, rel, "Historical session "+entry.Name(), time.Now()).Error; err != nil {
				panic(fmt.Sprintf("0.1.19: register legacy workspace %s: %v", rel, err))
			}
			var workspaceID int64
			if err := tx.Raw(`SELECT id FROM workspaces WHERE person_id = ? AND relative_path = ?`, personID, rel).Scan(&workspaceID).Error; err != nil {
				panic(fmt.Sprintf("0.1.19: read legacy workspace %s: %v", rel, err))
			}
			var works []legacyWork_0_1_19
			if err := tx.Raw(`SELECT id, session_id FROM works WHERE person_id = ? AND session_id = ?`, personID, sessionID).Scan(&works).Error; err != nil {
				panic(fmt.Sprintf("0.1.19: find legacy works for %s: %v", rel, err))
			}
			for _, work := range works {
				if err := tx.Exec(`INSERT INTO workspace_uses
					(workspace_id, source_type, source_id, role, created_at) VALUES (?, 1, ?, 1, ?)
					ON CONFLICT(workspace_id, source_type, source_id, role) DO NOTHING`,
					workspaceID, work.ID, time.Now()).Error; err != nil {
					panic(fmt.Sprintf("0.1.19: link historical work %d: %v", work.ID, err))
				}
			}
		}
		var unlinked []legacyWork_0_1_19
		if err := tx.Raw(`SELECT id, session_id FROM works WHERE person_id = ? AND NOT EXISTS (
			SELECT 1 FROM workspace_uses u WHERE u.source_type = 1 AND u.source_id = works.id AND u.role = 1)`,
			personID).Scan(&unlinked).Error; err != nil {
			panic(fmt.Sprintf("0.1.19: verify historical works for person %d: %v", personID, err))
		}
		for _, work := range unlinked {
			applogger.Error("0.1.19: historical Work directory missing; no Workspace association inferred",
				"person_id", personID, "work_id", work.ID, "session_id", work.SessionID)
		}
	}
}
