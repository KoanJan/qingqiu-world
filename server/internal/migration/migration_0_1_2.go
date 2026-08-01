package migration

import (
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

func migrate012() {
	if err := database.DB.AutoMigrate(&model.AgentEventBuffer{}); err != nil {
		panic(err)
	}
	if database.DB.Migrator().HasColumn(&model.ScheduledEvent{}, "trigger_message_id") {
		if err := database.DB.Migrator().DropColumn(&model.ScheduledEvent{}, "trigger_message_id"); err != nil {
			panic(err)
		}
	}
	if err := database.DB.Model(&model.AgentState{}).
		Where("sleep_since IS NULL").
		Update("sleep_since", "").Error; err != nil {
		panic(err)
	}
}
