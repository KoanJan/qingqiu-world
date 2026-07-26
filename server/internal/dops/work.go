package dops

import (
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// ListTaskWorks list all task work ids in the session
func ListTaskWorks(sessionID int64) ([]int64, error) {
	var workIDs []int64
	if err := database.DB.Model(&model.Work{}).
		Where("session_id = ? AND type = ?", sessionID, model.WorkTypeTask).
		Pluck("id", &workIDs).Error; err != nil {
		return nil, err
	}
	return workIDs, nil
}
