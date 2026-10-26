package migration

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

// removeLegacyInteractionSessionColumn retires the copied source Session ID.
// WorkID is the Interaction's owner, and Work.SessionID retains the optional
// originating conversation. The operation also accepts development databases
// already stamped 0.1.19 before this schema cleanup was added.
func removeLegacyInteractionSessionColumn() error {
	if !database.DB.Migrator().HasTable(&model.Interaction{}) ||
		!database.DB.Migrator().HasColumn(&model.Interaction{}, "session_id") {
		return nil
	}
	return database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DROP INDEX IF EXISTS idx_interactions_session_id").Error; err != nil {
			return fmt.Errorf("drop legacy Interaction Session index: %w", err)
		}
		if err := tx.Exec("ALTER TABLE interactions DROP COLUMN session_id").Error; err != nil {
			return fmt.Errorf("drop legacy Interaction Session column: %w", err)
		}
		return nil
	})
}
