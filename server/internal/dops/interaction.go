package dops

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// ListActivityInteractions reads one cursor page from the exact Works whose
// recorded causes belong to the requested Activity timeline. Request rows
// contain full LLM prompts and are excluded before their Data is read.
// A before cursor reads older rows; an after cursor reads newer rows.
func ListActivityInteractions(workIDs []int64, beforeInteractionID, afterInteractionID int64, limit int) ([]model.Interaction, bool, error) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("activity interaction limit must be positive")
	}
	if beforeInteractionID > 0 && afterInteractionID > 0 {
		return nil, false, fmt.Errorf("activity cursors cannot be combined")
	}
	if len(workIDs) == 0 {
		return []model.Interaction{}, false, nil
	}

	query := database.DB.Model(&model.Interaction{}).
		Select("id", "work_id", "type", "data", "created_at").
		Where("work_id IN ? AND type IN ?", workIDs, []int{model.InteractionTypeResponse, model.InteractionTypeGuidance})
	if beforeInteractionID > 0 {
		query = query.Where("id < ?", beforeInteractionID)
	} else if afterInteractionID > 0 {
		query = query.Where("id > ?", afterInteractionID)
	}

	interactions := make([]model.Interaction, 0, limit+1)
	order := "id DESC"
	if afterInteractionID > 0 {
		order = "id ASC"
	}
	if err := query.Order(order).Limit(limit + 1).Find(&interactions).Error; err != nil {
		return nil, false, err
	}

	hasMore := len(interactions) > limit
	if hasMore {
		interactions = interactions[:limit]
	}
	// Older and initial pages run newest-first; the UI receives chronological
	// rows for every page, including the ascending after-cursor page.
	if afterInteractionID == 0 {
		for left, right := 0, len(interactions)-1; left < right; left, right = left+1, right-1 {
			interactions[left], interactions[right] = interactions[right], interactions[left]
		}
	}
	return interactions, hasMore, nil
}
