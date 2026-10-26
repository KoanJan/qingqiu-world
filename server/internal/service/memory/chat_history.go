package memory

import (
	"fmt"
	"strings"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// ListObservedMessages loads bounded conversation context. currentIDs are the
// exact messages already read by the ongoing Comprehend pass; all other rows
// require a persisted Observation. The caller separately verifies membership.
func ListObservedMessages(personID, sessionID, maxMessageID int64, limit int, currentIDs []int64) ([]model.Message, error) {
	if personID <= 0 || !canReadSession(personID, sessionID) {
		return nil, fmt.Errorf("session %d is not accessible", sessionID)
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid message limit %d", limit)
	}
	query := database.DB.Table("messages").Select("DISTINCT messages.*").
		Joins("LEFT JOIN events ON events.event_type = ? AND events.ref_id = messages.id", model.EventTypeMessage).
		Joins("LEFT JOIN agent_observations obs ON obs.event_id = events.id AND obs.person_id = ?", personID).
		Where("messages.session_id = ?", sessionID)
	if maxMessageID > 0 {
		query = query.Where("messages.id <= ?", maxMessageID)
	}
	if len(currentIDs) > 0 {
		query = query.Where("obs.id IS NOT NULL OR messages.id IN ?", currentIDs)
	} else {
		query = query.Where("obs.id IS NOT NULL")
	}
	var rows []model.Message
	if err := query.Order("messages.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
	return rows, nil
}

// SearchObservedMessages uses the shared term index for local Chat history.
// It never scans a whole session into application memory.
func SearchObservedMessages(personID, sessionID, maxMessageID int64, keywords []string, limit int) ([]model.Message, error) {
	if personID <= 0 || !canReadSession(personID, sessionID) {
		return nil, fmt.Errorf("session %d is not accessible", sessionID)
	}
	if limit < 1 || limit > 20 {
		return nil, fmt.Errorf("invalid history limit %d", limit)
	}
	seen := make(map[int64]model.Message)
	if len(keywords) > 5 {
		applogger.Warn("chat history keyword list truncated", "person_id", personID, "session_id", sessionID, "keywords", len(keywords))
		keywords = keywords[:5]
	}
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(keyword)
		if len([]rune(keyword)) > 120 {
			applogger.Warn("chat history keyword truncated", "person_id", personID, "session_id", sessionID)
			keyword = string([]rune(keyword)[:120])
		}
		terms := searchTerms(keyword)
		if len(terms) == 0 {
			continue
		}
		var rows []model.Message
		query := database.DB.Table("messages").Select("DISTINCT messages.*").
			Joins("JOIN events ON events.event_type = ? AND events.ref_id = messages.id", model.EventTypeMessage).
			Joins("JOIN agent_observations obs ON obs.event_id = events.id AND obs.person_id = ?", personID).
			Where("messages.session_id = ?", sessionID)
		if maxMessageID > 0 {
			query = query.Where("messages.id <= ?", maxMessageID)
		}
		sub := database.DB.Table("memory_terms").Select("source_id").Where("source_kind = ? AND term IN ?", model.MemorySourceEvent, terms).
			Group("source_id").Having("COUNT(DISTINCT term) = ?", len(terms))
		if err := query.Where("events.id IN (?)", sub).Order("messages.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			seen[row.ID] = row
		}
	}
	rows := make([]model.Message, 0, len(seen))
	for _, row := range seen {
		rows = append(rows, row)
	}
	// The small candidate set is sorted deterministically after SQL limits.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].ID > rows[j-1].ID; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}
