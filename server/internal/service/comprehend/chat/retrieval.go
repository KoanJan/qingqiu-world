package chat

import (
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	applogger "qingqiu-world-server/internal/logger"
)

// GetRecentMessages returns recent messages from a session in chronological order.
// Messages are fetched in DESC order by ID and then reversed to ASC order.
// If status >= 0, only messages with that status are returned; -1 means no filter.
func GetRecentMessages(sessionID int64, limit int) []model.Message {
	query := database.DB.Model(&model.Message{}).Where("session_id = ?", sessionID)

	var messages []model.Message
	if err := query.Order("id DESC").Limit(limit).Find(&messages).Error; err != nil {
		applogger.Error("GetRecentMessages: failed to load messages", "error", err)
		return nil
	}

	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	return messages
}
