package dops

import (
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// ListInteractions list interactions of works
func ListInteractions(workIDs []int64) ([]model.Interaction, error) {
	// Query all interactions across all task works, ordered by creation time
	var interactions []model.Interaction
	if err := database.DB.Where("work_id IN ?", workIDs).
		Order("created_at ASC").Find(&interactions).Error; err != nil {
		return nil, err
	}
	return interactions, nil

}
