package comprehend

import (
	"strings"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

const defaultHistorySearchLimit = 5

// SearchMessagesByKeywordsBefore returns keyword-matched messages up to an ID boundary.
func SearchMessagesByKeywordsBefore(sessionIDs []int64, maxMessageID int64, keywords []string, limit int) []Segment {
	if len(sessionIDs) == 0 || len(keywords) == 0 {
		return nil
	}
	var messages []model.Message
	query := database.DB.Where("session_id IN ?", sessionIDs)
	if maxMessageID > 0 {
		query = query.Where("id <= ?", maxMessageID)
	}
	if err := query.Order("id DESC").Find(&messages).Error; err != nil {
		applogger.Error("failed to load messages for history search", "session_ids", sessionIDs, "error", err)
		return nil
	}
	lowerKeywords := make([]string, len(keywords))
	for i, keyword := range keywords {
		lowerKeywords[i] = strings.ToLower(keyword)
	}
	type match struct {
		message model.Message
		score   int
	}
	matches := make([]match, 0)
	for _, message := range messages {
		score := 0
		content := strings.ToLower(message.Content)
		for _, keyword := range lowerKeywords {
			if strings.Contains(content, keyword) {
				score++
			}
		}
		if score > 0 {
			matches = append(matches, match{message: message, score: score})
		}
	}
	for i := 1; i < len(matches); i++ {
		for j := i; j > 0 && matches[j].score > matches[j-1].score; j-- {
			matches[j], matches[j-1] = matches[j-1], matches[j]
		}
	}
	if limit > len(matches) {
		limit = len(matches)
	}
	segments := make([]Segment, 0, limit)
	for _, item := range matches[:limit] {
		segments = append(segments, Segment{MessageID: item.message.ID, Content: item.message.Content, Source: SourceChatHistory})
	}
	return segments
}
