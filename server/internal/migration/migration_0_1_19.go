package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// migrate_0_1_19 registers real legacy directories without moving files or
// inventing an Action that created them. A Work is linked only when its former
// session directory exists; missing directories are reported as data loss.
// It also removes the Interaction Session copy after Work ownership is stable.
func migrate_0_1_19() {
	var persons []model.Person
	if err := database.DB.Find(&persons).Error; err != nil {
		panic(fmt.Sprintf("0.1.19: list persons: %v", err))
	}
	for _, person := range persons {
		root := legacySessionWorkspaceRoot(person.ID)
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
				applogger.Warn("0.1.19: unrecognized legacy work directory", "person_id", person.ID, "name", entry.Name())
				continue
			}
			rel := filepath.Join("work", entry.Name())
			var record model.Workspace
			if err := database.DB.Where("person_id = ? AND relative_path = ?", person.ID, rel).FirstOrCreate(&record,
				model.Workspace{PersonID: person.ID, RelativePath: rel, Name: "Historical session " + entry.Name(), Purpose: ""}).Error; err != nil {
				panic(fmt.Sprintf("0.1.19: register legacy workspace %s: %v", rel, err))
			}
			var works []model.Work
			if err := database.DB.Where("person_id = ? AND session_id = ?", person.ID, sessionID).Find(&works).Error; err != nil {
				panic(fmt.Sprintf("0.1.19: find legacy works for %s: %v", rel, err))
			}
			for _, work := range works {
				use := model.WorkspaceUse{WorkspaceID: record.ID, SourceType: model.WorkspaceUseWork, SourceID: work.ID, Role: model.WorkspaceUseDefault}
				if err := database.DB.Where("workspace_id = ? AND source_type = ? AND source_id = ? AND role = ?", use.WorkspaceID, use.SourceType, use.SourceID, use.Role).FirstOrCreate(&use).Error; err != nil {
					panic(fmt.Sprintf("0.1.19: link historical work %d: %v", work.ID, err))
				}
			}
		}
		var unlinked []model.Work
		if err := database.DB.Where(`person_id = ? AND NOT EXISTS (
			SELECT 1 FROM workspace_uses u WHERE u.source_type = ? AND u.source_id = works.id AND u.role = ?)`,
			person.ID, model.WorkspaceUseWork, model.WorkspaceUseDefault).Find(&unlinked).Error; err != nil {
			panic(fmt.Sprintf("0.1.19: verify historical works for person %d: %v", person.ID, err))
		}
		for _, work := range unlinked {
			applogger.Error("0.1.19: historical Work directory missing; no Workspace association inferred",
				"person_id", person.ID, "work_id", work.ID, "session_id", work.SessionID)
		}
	}
	if err := removeLegacyInteractionSessionColumn(); err != nil {
		panic(fmt.Sprintf("0.1.19: remove obsolete Interaction Session column: %v", err))
	}
	if err := migrateRetiredJinshuTypes(); err != nil {
		panic(fmt.Sprintf("0.1.19: compact retired Jinshu types: %v", err))
	}
	if err := migrateDurableEventTypes(); err != nil {
		panic(fmt.Sprintf("0.1.19: compact durable Event types: %v", err))
	}
}
