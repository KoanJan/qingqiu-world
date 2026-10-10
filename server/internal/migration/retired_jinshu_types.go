package migration

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

const retiredJinshuTypeMigration = "0.1.19 compact retired Jinshu Action and buffered Event types"
// Keep this marker stable across development revisions so converted rows are
// never shifted a second time.
const durableEventTypeMigration = "0.1.19 compact durable Event types and reuse system notifications"

// migrateRetiredJinshuTypes compacts the two runtime enums exactly once.
// Old InspectJinshu Actions lose their type because the action is no longer
// executable. Durable Event types are converted separately; only the
// unconsumed read-completed queue delivery is discarded here.
func migrateRetiredJinshuTypes() error {
	tx := database.DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback()

	var applied int64
	if err := tx.Model(&model.DBVersion{}).Where("description = ?", retiredJinshuTypeMigration).Count(&applied).Error; err != nil {
		return fmt.Errorf("check retired Jinshu type migration: %w", err)
	}
	if applied > 0 {
		return nil
	}

	// The old Action enum used 7 for InspectJinshu and 8..12 for the
	// following actions. One CASE update avoids interpreting shifted rows twice.
	if err := tx.Exec(`UPDATE actions SET type = CASE WHEN type = 7 THEN -1 ELSE type - 1 END WHERE type BETWEEN 7 AND 12`).Error; err != nil {
		return fmt.Errorf("compact Action types: %w", err)
	}
	if err := tx.Exec(`UPDATE actions SET status = ? WHERE type = -1 AND status = ?`, model.ActionStatusEnded, model.ActionStatusInProgress).Error; err != nil {
		return fmt.Errorf("end retired Actions: %w", err)
	}

	// AgentEventType used 9 for JinshuReadCompleted. A buffered delivery is
	// pending runtime work, so remove it before shifting later queue types.
	if err := tx.Exec(`DELETE FROM agent_event_buffers WHERE event_type = 9`).Error; err != nil {
		return fmt.Errorf("discard buffered Jinshu read completions: %w", err)
	}
	if err := tx.Exec(`UPDATE agent_event_buffers SET event_type = event_type - 1 WHERE event_type BETWEEN 10 AND 15`).Error; err != nil {
		return fmt.Errorf("compact buffered Event types: %w", err)
	}
	if err := tx.Create(&model.DBVersion{Version: "0.1.19", Description: retiredJinshuTypeMigration}).Error; err != nil {
		return fmt.Errorf("record retired Jinshu type migration: %w", err)
	}
	return tx.Commit().Error
}

// migrateDurableEventTypes retires the persisted read result type without
// deleting its occurrence or changing any Event ID referenced by a Decision.
// It runs after the earlier queue migration, including on development databases
// that already recorded that migration's marker.
func migrateDurableEventTypes() error {
	tx := database.DB.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback()

	var applied int64
	if err := tx.Model(&model.DBVersion{}).Where("description = ?", durableEventTypeMigration).Count(&applied).Error; err != nil {
		return fmt.Errorf("check durable Event type migration: %w", err)
	}
	if applied > 0 {
		return nil
	}

	// Type 7 was JinshuReadCompleted. Its historical payload and Event ID
	// remain available, but its type is no longer a valid runtime type.
	if err := tx.Exec(`UPDATE events SET event_type = CASE WHEN event_type = 7 THEN -1 ELSE event_type - 1 END WHERE event_type BETWEEN 7 AND 12`).Error; err != nil {
		return fmt.Errorf("compact durable Event types: %w", err)
	}
	if err := tx.Create(&model.DBVersion{Version: "0.1.19", Description: durableEventTypeMigration}).Error; err != nil {
		return fmt.Errorf("record durable Event type migration: %w", err)
	}
	return tx.Commit().Error
}
