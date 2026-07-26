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
