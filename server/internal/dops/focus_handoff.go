package dops

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// CreateFocusHandoff persists runtime-owned Focus continuity metadata.
func CreateFocusHandoff(record *model.FocusHandoff) error {
	if record == nil {
		return fmt.Errorf("focus handoff is required")
	}
	if err := database.DB.Create(record).Error; err != nil {
		return fmt.Errorf("create focus handoff: %w", err)
	}
	return nil
}
