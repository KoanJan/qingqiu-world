package dops

import (
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// CreateMessage creates a message in a session
func CreateMessage(message *model.Message) error {
	return database.DB.Select("SessionID", "PersonID", "Content").Create(message).Error
}

// ListMessagesBySessionID list messages by session_id
func ListMessagesBySessionID(sessionID int64) ([]model.Message, error) {
	var messages []model.Message
	if err := database.DB.Where("session_id = ?", sessionID).Order("created_at ASC").Find(&messages).Error; err != nil {
		return nil, err
	}
	return messages, nil
}

// ListMessagesInRange returns messages in the half-open ID range (after, until].
func ListMessagesInRange(sessionID, afterMessageID, untilMessageID int64) ([]model.Message, error) {
	var messages []model.Message
	err := database.DB.Where("session_id = ? AND id > ? AND id <= ?", sessionID, afterMessageID, untilMessageID).
		Order("id ASC").
		Find(&messages).Error
	return messages, err
}

// GetMaxMessageID returns the latest message ID in a session, or zero when empty.
func GetMaxMessageID(sessionID int64) (int64, error) {
	var maxMessageID int64
	err := database.DB.Model(&model.Message{}).
		Where("session_id = ?", sessionID).
		Select("COALESCE(MAX(id), 0)").
		Scan(&maxMessageID).Error
	return maxMessageID, err
}

// GetPersonNames returns the names indexed by person ID.
func GetPersonNames(personIDs []int64) (map[int64]string, error) {
	if len(personIDs) == 0 {
		return map[int64]string{}, nil
	}
	var persons []model.Person
	if err := database.DB.Where("id IN ?", personIDs).Find(&persons).Error; err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(persons))
	for _, person := range persons {
		names[person.ID] = person.Name
	}
	return names, nil
}
