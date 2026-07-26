package comprehend

import (
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	applogger "qingqiu-world-server/internal/logger"
)

// Segment source constants
const (
	SourceChatHistory = iota + 1
	SourceKnowledgeBase
)

// Segment represents a retrieved context segment used in prompt assembly.
// MessageID is set for chat-history segments so the memory system can
// locate the corresponding observation and apply a retrieval hit.
type Segment struct {
	MessageID int64  `json:"message_id"`
	Content   string `json:"content"`
	Source    int    `json:"source"`
}

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
