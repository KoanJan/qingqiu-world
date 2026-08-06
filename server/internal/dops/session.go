package dops

import (
	"fmt"
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

// GetSession by sessionID
func GetSession(sessionID int64) (*model.Session, error) {
	var session model.Session
	if err := database.DB.First(&session, sessionID).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

// HasInteractions checks whether any task interaction records exist for the given session.
func HasInteractions(sessionID int64) (bool, error) {
	var count int64
	if err := database.DB.Model(&model.Interaction{}).
		Where("session_id = ?", sessionID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetSessionParticipantsByPersonType returns participant_sessions joined with persons filtered by type.
func GetSessionParticipantsByPersonType(sessionID int64, personType int) ([]model.ParticipantSession, error) {
	var p []model.ParticipantSession
	err := database.DB.Where(
		"session_id = ? AND participant_id IN (SELECT id FROM persons WHERE type = ?)",
		sessionID, personType,
	).Find(&p).Error
	return p, err
}

// GetSessionParticipantsByPersonTypeMulti is like GetSessionParticipantsByPersonType but for multiple sessions.
func GetSessionParticipantsByPersonTypeMulti(sessionIDs []int64, personType int) ([]model.ParticipantSession, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	var p []model.ParticipantSession
	err := database.DB.Where(
		"session_id IN ? AND participant_id IN (SELECT id FROM persons WHERE type = ?)",
		sessionIDs, personType,
	).Find(&p).Error
	return p, err
}

// GetSessionAIParticipantIDs returns the person IDs of all AI participants in a session.
func GetSessionAIParticipantIDs(sessionID int64) ([]int64, error) {
	var ids []int64
	err := database.DB.Model(&model.ParticipantSession{}).
		Where("session_id = ? AND participant_id IN (SELECT id FROM persons WHERE type = ?)", sessionID, model.PersonTypeAI).
		Pluck("participant_id", &ids).Error
	return ids, err
}

// GetSessionAIParticipantIDsMulti is like GetSessionAIParticipantIDs but for multiple sessions.
func GetSessionAIParticipantIDsMulti(sessionIDs []int64) ([]int64, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	var ids []int64
	err := database.DB.Model(&model.ParticipantSession{}).
		Where("session_id IN ? AND participant_id IN (SELECT id FROM persons WHERE type = ?)", sessionIDs, model.PersonTypeAI).
		Pluck("participant_id", &ids).Error
	return ids, err
}

// UpdateLastReadMessageID records the id of last read message in the session for the person
func UpdateLastReadMessageID(sessionID, personID, messageID int64) error {
	return database.DB.Model(&model.ParticipantSession{}).
		Where("session_id = ? AND participant_id = ?", sessionID, personID).
		Update("last_read_message_id", messageID).Error
}

// GetParticipantSession returns a person's participation record for a session.
func GetParticipantSession(sessionID, personID int64) (*model.ParticipantSession, error) {
	var participantSession model.ParticipantSession
	if err := database.DB.Where("session_id = ? AND participant_id = ?", sessionID, personID).
		First(&participantSession).Error; err != nil {
		return nil, err
	}
	return &participantSession, nil
}

// AdvanceLastReadMessageID advances the read marker without moving it backward.
func AdvanceLastReadMessageID(sessionID, personID, messageID int64) error {
	return database.DB.Model(&model.ParticipantSession{}).
		Where("session_id = ? AND participant_id = ? AND last_read_message_id < ?", sessionID, personID, messageID).
		Update("last_read_message_id", messageID).Error
}

// ListAIParticipants returns all AIParticipants in the session
func ListAIParticipants(sessionID int64) (participants []model.ParticipantSession, err error) {
	err = database.DB.
		Joins("JOIN persons ON persons.id = participant_sessions.participant_id AND persons.type = ?", model.PersonTypeAI).
		Where("participant_sessions.session_id = ?", sessionID).
		Find(&participants).Error
	return
}

// CreateSession with a new message
func CreateSession(session *model.Session, firstMessage *model.Message, fromPersonID, toPersonID int64) error {
	// Create all session resources in a single transaction
	return database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(session).Error; err != nil {
			return err
		}
		firstMessage.SessionID = session.ID

		if err := tx.Create(&model.ParticipantSession{
			SessionID:     session.ID,
			ParticipantID: fromPersonID,
			Role:          model.ParticipantRoleOwner,
			Status:        model.ParticipantStatusIdle,
		}).Error; err != nil {
			return err
		}

		if err := tx.Create(&model.ParticipantSession{
			SessionID:     session.ID,
			ParticipantID: toPersonID,
			Role:          model.ParticipantRoleMember,
			Status:        model.ParticipantStatusIdle,
		}).Error; err != nil {
			return err
		}

		if err := tx.Select("SessionID", "PersonID", "Content").Create(firstMessage).Error; err != nil {
			return err
		}

		if err := tx.Model(&model.ParticipantSession{}).
			Where("session_id = ? AND participant_id = ?", session.ID, fromPersonID).
			Update("last_read_message_id", firstMessage.ID).Error; err != nil {
			return err
		}

		return nil
	})
}

// CreateDirectSession creates a new 1v1 Session between two Persons without
// a first message. Used by the runtime when an Agent decides to start a new
// conversation (create_and_send). The first message is composed separately by
// ComposeMessageWork and submitted through the draft commit channel.
//
// The initiator is recorded as the session owner (ParticipantRoleOwner); the
// recipient is a regular member. Both start in idle status with no messages
// read (last_read_message_id = 0).
//
// Unlike CreateSession (which is the HTTP path for human-initiated sessions
// and carries the human's first message), this function:
//   - Does NOT create a first message — the agent's ComposeMessageWork will
//     produce the first message via the draft → commit channel.
//   - Does NOT advance last_read_message_id — there are no messages yet.
//
// Returns the newly created session's ID. The caller is responsible for
// composing and committing the first message.
func CreateDirectSession(initiatorPersonID, recipientPersonID int64) (int64, error) {
	var sessionID int64
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		session := model.Session{
			Title: "",
		}
		if err := tx.Create(&session).Error; err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		sessionID = session.ID

		if err := tx.Create(&model.ParticipantSession{
			SessionID:     session.ID,
			ParticipantID: initiatorPersonID,
			Role:          model.ParticipantRoleOwner,
			Status:        model.ParticipantStatusIdle,
		}).Error; err != nil {
			return fmt.Errorf("create initiator participant: %w", err)
		}

		if err := tx.Create(&model.ParticipantSession{
			SessionID:     session.ID,
			ParticipantID: recipientPersonID,
			Role:          model.ParticipantRoleMember,
			Status:        model.ParticipantStatusIdle,
		}).Error; err != nil {
			return fmt.Errorf("create recipient participant: %w", err)
		}

		return nil
	})
	if err != nil {
		return 0, err
	}
	return sessionID, nil
}

// DeleteSessionCascade deletes a session and all associated data in a transaction.
// Returns the first AI agent's PersonID for caller's workspace cleanup, and 0 for
// the legacy agentConfigID (caller ignores it).
func DeleteSessionCascade(sessionID int64) (personID int64, agentConfigID int64, err error) {
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		var sess model.Session
		if err := tx.First(&sess, sessionID).Error; err != nil {
			return fmt.Errorf("session %d not found: %w", sessionID, err)
		}

		// Resolve the first AI agent's PersonID from participant_sessions
		// for workspace cleanup.
		var aiPersonID int64
		if err := tx.Raw(`SELECT ac.person_id FROM participant_sessions ps
			JOIN persons p ON p.id = ps.participant_id AND p.type = 1
			JOIN agent_configs ac ON ac.person_id = p.id
			WHERE ps.session_id = ?
			LIMIT 1`, sessionID).Scan(&aiPersonID).Error; err != nil {
			applogger.Error("failed to find agent person for session during cleanup",
				"session_id", sessionID, "error", err)
		}
		personID = aiPersonID

		tables := []interface{}{
			&model.Work{}, &model.MessageDraft{}, &model.Interaction{},
			&model.AgentNarrative{}, &model.Summary{},
			&model.ParticipantSession{}, &model.Message{},
		}
		for _, table := range tables {
			if err := tx.Where("session_id = ?", sessionID).Delete(table).Error; err != nil {
				return err
			}
		}
		if err := tx.Delete(&sess).Error; err != nil {
			return err
		}

		return nil
	})

	return personID, agentConfigID, err
}

// GetFirstAIParticipantID returns the first AI participant's person ID in a session.
func GetFirstAIParticipantID(sessionID int64) int64 {
	var personID int64
	database.DB.Raw(`SELECT ps.participant_id FROM participant_sessions ps
		JOIN persons p ON p.id = ps.participant_id AND p.type = 1
		WHERE ps.session_id = ?
		LIMIT 1`, sessionID).Scan(&personID)
	return personID
}

// IsParticipant returns true if the given person is a participant in the session.
func IsParticipant(sessionID, personID int64) (bool, error) {
	var count int64
	err := database.DB.Model(&model.ParticipantSession{}).
		Where("session_id = ? AND participant_id = ?", sessionID, personID).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetSessionOtherParticipant returns the conversation partner of selfPersonID
// in a 1v1 session — the participant other than selfPersonID. This resolves
// "the person you are talking to" from the session's actual participants
// rather than a hardcoded human user, so A2A sessions correctly identify the
// other agent as the partner.
//
// In a 1v1 session there is exactly one other participant. If the session has
// multiple other participants (future group chat), the first is returned;
// 0.1.3 does not support group chat. When no other participant exists,
// PersonID is 0 and Name is "" — callers should treat this as "no partner".
func GetSessionOtherParticipant(sessionID, selfPersonID int64) (*SessionMember, error) {
	var sm SessionMember
	err := database.DB.Raw(`SELECT ps.participant_id AS person_id, p.name, p.avatar
		FROM participant_sessions ps
		JOIN persons p ON p.id = ps.participant_id
		WHERE ps.session_id = ? AND ps.participant_id != ?
		LIMIT 1`, sessionID, selfPersonID).Scan(&sm).Error
	if err != nil {
		return nil, err
	}
	return &sm, nil
}

// GetParticipatedSessions returns a set of session IDs (as map keys) that the
// given person participates in, from the provided candidate session IDs.
// Used by the session list API to mark which sessions the current user is in.
func GetParticipatedSessions(personID int64, sessionIDs []int64) (map[int64]bool, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	var ids []int64
	err := database.DB.Model(&model.ParticipantSession{}).
		Where("participant_id = ? AND session_id IN ?", personID, sessionIDs).
		Pluck("session_id", &ids).Error
	if err != nil {
		return nil, err
	}
	result := make(map[int64]bool, len(ids))
	for _, id := range ids {
		result[id] = true
	}
	return result, nil
}
