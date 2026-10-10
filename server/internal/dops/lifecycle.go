package dops

import (
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

// RequireActivePersonTx checks the final write boundary for an acting or receiving person.
func RequireActivePersonTx(tx *gorm.DB, personID int64) error {
	var person model.Person
	if err := tx.Select("id", "status").First(&person, personID).Error; err != nil {
		return err
	}
	if person.Status != model.PersonStatusActive {
		return fmt.Errorf("person %d is deceased", personID)
	}
	return nil
}

// RequireActiveSessionTx checks the final write boundary for a conversation.
func RequireActiveSessionTx(tx *gorm.DB, sessionID int64) error {
	var session model.Session
	if err := tx.Select("id", "status").First(&session, sessionID).Error; err != nil {
		return err
	}
	if session.Status != model.SessionStatusActive {
		return fmt.Errorf("session %d is deleted", sessionID)
	}
	return nil
}

// MarkSessionDeleted closes an active conversation while preserving every record it produced.
func MarkSessionDeleted(sessionID int64) (bool, error) {
	changed := false
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.Session{}).
			Where("id = ? AND status = ?", sessionID, model.SessionStatusActive).
			Update("status", model.SessionStatusDeleted)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var session model.Session
			return tx.Select("id", "status").First(&session, sessionID).Error
		}
		changed = true
		return tx.Model(&model.ParticipantSession{}).
			Where("session_id = ?", sessionID).
			Update("status", model.ParticipantStatusIdle).Error
	})
	return changed, err
}

// UpdateActiveSessionTitle changes only an open conversation. The condition
// lives on the write statement so deletion cannot race a handler's earlier read.
func UpdateActiveSessionTitle(sessionID int64, title string) error {
	result := database.DB.Model(&model.Session{}).
		Where("id = ? AND status = ?", sessionID, model.SessionStatusActive).
		Update("title", title)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("session %d is unavailable for editing: %w", sessionID, gorm.ErrRecordNotFound)
	}
	return nil
}

// MarkAIPersonDeceasedTx changes one AI identity inside the transaction that records its world event.
func MarkAIPersonDeceasedTx(tx *gorm.DB, personID int64) (bool, error) {
	result := tx.Model(&model.Person{}).
		Where("id = ? AND type = ? AND status = ?", personID, model.PersonTypeAI, model.PersonStatusActive).
		Update("status", model.PersonStatusDeceased)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		var person model.Person
		if err := tx.Select("id", "type", "status").First(&person, personID).Error; err != nil {
			return false, err
		}
		if person.Type != model.PersonTypeAI {
			return false, fmt.Errorf("person %d is not an agent", personID)
		}
		return false, nil
	}
	if err := tx.Model(&model.ParticipantSession{}).
		Where("participant_id = ?", personID).
		Update("status", model.ParticipantStatusIdle).Error; err != nil {
		return false, err
	}
	if err := tx.Model(&model.Action{}).
		Where("type = ? AND status = ? AND decision_id IN (SELECT id FROM decisions WHERE person_id = ?)", model.ActionTypeWaitForExecutionSlot, model.ActionStatusInProgress, personID).
		Update("status", model.ActionStatusEnded).Error; err != nil {
		return false, err
	}
	// Buffered occurrences are delivery opportunities, not observations.
	// A deceased person can no longer consume them; their source Events remain.
	if err := tx.Where("person_id = ?", personID).Delete(&model.AgentEventBuffer{}).Error; err != nil {
		return false, err
	}
	err := tx.Model(&model.ScheduledEvent{}).
		Where("person_id = ? AND status = ?", personID, model.ScheduledEventStatusPending).
		Update("status", model.ScheduledEventStatusCancelled).Error
	return err == nil, err
}
