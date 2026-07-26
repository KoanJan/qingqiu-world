package chatcontext

import (
	"strings"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/comprehend"

	applogger "qingqiu-world-server/internal/logger"
)

// SearchMessagesByKeywords searches messages in the given sessions for keyword matches.
// Each keyword is matched case-insensitively via substring. Results are ordered by
// recency (newest first) and limited to the specified count.
//
// Parameters:
//   - sessionIDs: sessions to search within
//   - keywords: list of keywords for case-insensitive substring matching
//   - limit: maximum number of results to return
//
// Returns matched messages as comprehend.Segment values with SourceChatHistory.
func SearchMessagesByKeywords(sessionIDs []int64, keywords []string, limit int) []comprehend.Segment {
	if len(sessionIDs) == 0 || len(keywords) == 0 {
		return nil
	}

	var messages []model.Message
	if err := database.DB.
		Where("session_id IN ?", sessionIDs).
		Order("created_at DESC").
		Find(&messages).Error; err != nil {
		applogger.Error("SearchMessagesByKeywords: failed to load messages", "session_ids", sessionIDs, "error", err)
		return nil
	}

	// Build lowercase keywords for case-insensitive matching
	lowerKeywords := make([]string, len(keywords))
	for i, kw := range keywords {
		lowerKeywords[i] = strings.ToLower(kw)
	}

	// Filter and score: count how many keywords match each message
	type match struct {
		msg   model.Message
		score int // number of matched keywords
	}
	var matches []match

	for _, msg := range messages {
		contentLower := strings.ToLower(msg.Content)
		hitCount := 0
		for _, kw := range lowerKeywords {
			if strings.Contains(contentLower, kw) {
				hitCount++
			}
		}
		if hitCount > 0 {
			matches = append(matches, match{msg: msg, score: hitCount})
		}
	}

	if len(matches) == 0 {
		return nil
	}

	// Sort by score (descending), keep recency order (DESC from DB) as secondary sort
	// Using simple insertion sort since match count is small
	for i := 1; i < len(matches); i++ {
		j := i
		for j > 0 && matches[j].score > matches[j-1].score {
			matches[j], matches[j-1] = matches[j-1], matches[j]
			j--
		}
	}

	// Limit results
	if limit > len(matches) {
		limit = len(matches)
	}

	segments := make([]comprehend.Segment, 0, limit)
	for i := 0; i < limit; i++ {
		m := matches[i]
		segments = append(segments, comprehend.Segment{
			MessageID: m.msg.ID,
			Content:   m.msg.Content,
			Source:    comprehend.SourceChatHistory,
		})
	}

	applogger.Info("Keyword search completed",
		"session_count", len(sessionIDs),
		"keyword_count", len(keywords),
		"total_messages", len(messages),
		"matched", len(matches),
		"returned", len(segments),
	)

	return segments
}
