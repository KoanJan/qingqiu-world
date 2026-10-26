package memory

import (
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// LoadObservedSessionContext returns the cached conversation background and a
// bounded recent window visible to one person at the given message boundary.
// currentIDs admit only messages already accepted by the current Comprehend
// pass; callers after that pass has committed its observations pass nil.
func LoadObservedSessionContext(personID, sessionID, maxMessageID int64, recentCount int, currentIDs []int64) ([]model.Message, int, string, error) {
	messages, err := ListObservedMessages(personID, sessionID, maxMessageID, recentCount, currentIDs)
	if err != nil {
		return nil, -1, "", err
	}
	var summary model.Summary
	if err := database.DB.Where("session_id = ?", sessionID).Order("version DESC").First(&summary).Error; err != nil {
		return messages, -1, "", nil
	}
	if !summaryRangeObserved(sessionID, personID, summary.Version, maxMessageID) {
		applogger.Error("session context summary contains messages outside observed range", "session_id", sessionID, "person_id", personID, "summary_version", summary.Version)
		return messages, -1, "", nil
	}
	var narrative model.AgentNarrative
	if err := database.DB.Where("session_id = ? AND person_id = ?", sessionID, personID).
		Order("summary_version DESC").First(&narrative).Error; err != nil {
		return messages, summary.Version, "", nil
	}
	return messages, summary.Version, narrative.Content, nil
}

// summaryRangeObserved checks that a shared summary's entire message range
// was observed by this person before its narrative is used as background.
func summaryRangeObserved(sessionID, personID int64, version int, maxMessageID int64) bool {
	if version < 1 {
		return false
	}
	if maxMessageID > 0 {
		var summaryEnd int64
		if err := database.DB.Table("messages").Select("id").Where("session_id = ?", sessionID).
			Order("id").Offset(version - 1).Limit(1).Scan(&summaryEnd).Error; err != nil || summaryEnd == 0 || summaryEnd > maxMessageID {
			return false
		}
	}
	var observed int64
	err := database.DB.Table("messages").Select("COUNT(DISTINCT messages.id)").
		Joins("JOIN events ON events.event_type = ? AND events.ref_id = messages.id", model.EventTypeMessage).
		Joins("JOIN agent_observations obs ON obs.event_id = events.id AND obs.person_id = ?", personID).
		Where("messages.id IN (SELECT id FROM messages WHERE session_id = ? ORDER BY id LIMIT ?)", sessionID, version).
		Scan(&observed).Error
	return err == nil && observed == int64(version)
}
