package migration

import (
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	applogger "qingqiu-world-server/internal/logger"
)

func migrate_0_1_5() {
	// Drop the workspace.type column — WorkType was removed in 0.1.5.
	if database.DB.Migrator().HasColumn(&model.Work{}, "type") {
		if err := database.DB.Migrator().DropColumn(&model.Work{}, "type"); err != nil {
			applogger.Error("migration 0.1.5: failed to drop works.type column", "error", err)
			panic(err)
		}
	}
}
