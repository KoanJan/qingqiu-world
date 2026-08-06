// Package migration handles incremental and full-init database data migrations.
package migration

import (
	"time"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// migrate_0_1_1 handles the 0.1.1 data migration: ensures every existing AI person
// has a corresponding agent_states row with default energy (100).
//
// GORM AutoMigrate already creates the table, so this only seeds rows for
// persons that lack an agent_states entry. Idempotent — safe to run multiple times.
//
// Note: LastRecoveredDate is set to UTC today as a placeholder. The next
// RecoverEnergy call will correct it to the global fixed timezone date.
func migrate_0_1_1() {
	var personIDs []int64
	err := database.DB.Model(&model.Person{}).
		Where("type = ?", 1).
		Where("id NOT IN (?)", database.DB.Model(&model.AgentState{}).Select("person_id")).
		Pluck("id", &personIDs).Error
	if err != nil {
		applogger.Error("migration 0.1.1: failed to query AI persons without agent_state", "error", err)
		return
	}

	if len(personIDs) == 0 {
		applogger.Info("migration 0.1.1: no AI persons need agent_state seeding")
		return
	}

	today := time.Now().UTC().Format("2006-01-02")
	for _, pid := range personIDs {
		if err := database.DB.Create(&model.AgentState{
			PersonID:          pid,
			Energy:            100,
			LastRecoveredDate: today,
		}).Error; err != nil {
			applogger.Error("migration 0.1.1: failed to create agent_state", "person_id", pid, "error", err)
		}
	}
	applogger.Info("migration 0.1.1: seeded agent_states for existing AI persons", "count", len(personIDs))
}
