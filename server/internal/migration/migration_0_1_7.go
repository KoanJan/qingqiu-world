package migration

import (
	"qingqiu-world-server/internal/database"

	applogger "qingqiu-world-server/internal/logger"
)

// migrate_0_1_7 renames the legacy agent_deliveries table into jinshus and
// reshapes it to the new Jinshu model: remark becomes description, session_id
// and paths are dropped, and topic is introduced (defaulting to empty).
//
// GORM AutoMigrate already created the empty jinshus table with the new schema
// before this runs, so we only need to copy surviving rows over and drop the
// legacy table. File copies under the old session-scoped received/ directories
// are intentionally not migrated (dev-stage data, only audit records existed).
func migrate_0_1_7() {
	migrator := database.DB.Migrator()

	if !migrator.HasTable("agent_deliveries") {
		return
	}

	if err := database.DB.Exec(`
		INSERT INTO jinshus (from_person_id, to_person_id, topic, description, created_at)
		SELECT from_person_id, to_person_id, '', remark, created_at
		FROM agent_deliveries
	`).Error; err != nil {
		applogger.Error("migration 0.1.7: failed to migrate agent_deliveries to jinshus", "error", err)
		panic(err)
	}

	if err := migrator.DropTable("agent_deliveries"); err != nil {
		applogger.Error("migration 0.1.7: failed to drop agent_deliveries table", "error", err)
		panic(err)
	}
}
