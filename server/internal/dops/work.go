package dops

import (
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// ListSessionActivityWorks returns the minimal Work projection required to
// attribute session activity to the agent that produced it.
func ListSessionActivityWorks(sessionID int64) ([]model.Work, error) {
	var works []model.Work
	if err := database.DB.Model(&model.Work{}).
		Select("id", "person_id").
		Where("session_id = ?", sessionID).
		Find(&works).Error; err != nil {
		return nil, err
	}
	return works, nil
}
