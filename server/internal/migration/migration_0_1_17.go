package migration

import (
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// migrate_0_1_17 backfills the Message expression field after AutoMigrate has
// added the TTS tables and the non-null column for existing installations.
func migrate_0_1_17() {
	if !database.DB.Migrator().HasColumn(&model.Message{}, "expression_instruction") {
		if err := database.DB.Migrator().AddColumn(&model.Message{}, "ExpressionInstruction"); err != nil {
			applogger.Error("migration 0.1.17: failed to add message expression instruction", "error", err)
			panic(err)
		}
	}
	if err := database.DB.Model(&model.Message{}).Where("expression_instruction IS NULL").Update("expression_instruction", "").Error; err != nil {
		applogger.Error("migration 0.1.17: failed to backfill message expression instruction", "error", err)
		panic(err)
	}
	applogger.Info("migration 0.1.17: speech schema ready")
}
