package dops

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// ListSessionActivityInteractions returns one newest-first cursor page of
// only the interactions that can become visible activity events. Request
// interactions contain full LLM prompts but are never displayed, so this
// query excludes them before SQLite reads their Data payloads.
func ListSessionActivityInteractions(sessionID, beforeInteractionID int64, limit int) ([]model.Interaction, bool, error) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("activity interaction limit must be positive")
	}

	query := database.DB.Model(&model.Interaction{}).
		Select("interactions.id", "interactions.work_id", "interactions.type", "interactions.data", "interactions.created_at").
		Joins("JOIN works ON works.id = interactions.work_id").
		Where("works.session_id = ? AND interactions.type IN ?", sessionID, []int{model.InteractionTypeResponse, model.InteractionTypeGuidance})
	if beforeInteractionID > 0 {
		query = query.Where("interactions.id < ?", beforeInteractionID)
	}

	interactions := make([]model.Interaction, 0, limit+1)
	if err := query.Order("interactions.id DESC").Limit(limit + 1).Find(&interactions).Error; err != nil {
		return nil, false, err
	}

	hasMore := len(interactions) > limit
	if hasMore {
		interactions = interactions[:limit]
	}
	// The cursor query runs newest-first for an efficient bounded read, while
	// the UI timeline remains chronological within each page.
	for left, right := 0, len(interactions)-1; left < right; left, right = left+1, right-1 {
		interactions[left], interactions[right] = interactions[right], interactions[left]
	}
	return interactions, hasMore, nil
}
