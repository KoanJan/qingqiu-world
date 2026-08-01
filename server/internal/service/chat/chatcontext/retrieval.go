package chatcontext

import (
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/comprehend"

	applogger "qingqiu-world-server/internal/logger"
)

// RetrievalResult holds all context components retrieved for chat processing.
type RetrievalResult struct {
	RecentMessages   []model.Message      `json:"recent_messages"`
	RelevantSegments []comprehend.Segment `json:"relevant_segments"`
	SummaryVersion   int                  `json:"summary_version"`
	Narrative        string               `json:"narrative"`
}

// buildSummaryAndNarrative extracts summary version and cached narrative
// from the new split models (Summary + AgentNarrative).
// Returns (summaryVersion, narrative). summaryVersion is -1 if no summary exists.
func buildSummaryAndNarrative(sessionID, personID int64) (int, string) {
	latestSummary := getLatestSummaryBySessionID(sessionID)
	if latestSummary == nil {
		return -1, ""
	}

	latestNarrative := getLatestNarrativeByIDs(sessionID, personID)
	if latestNarrative == nil {
		return latestSummary.Version, ""
	}

	return latestSummary.Version, latestNarrative.Content
}

// getLatestSummaryBySessionID returns the latest summary for a session.
func getLatestSummaryBySessionID(sessionID int64) *model.Summary {
	var s model.Summary
	err := database.DB.Where("session_id = ?", sessionID).Order("version DESC").First(&s).Error
	if err != nil {
		return nil
	}
	return &s
}

// getLatestNarrativeByIDs returns the latest narrative for a (session, agent).
func getLatestNarrativeByIDs(sessionID, personID int64) *model.AgentNarrative {
	var n model.AgentNarrative
	err := database.DB.Where("session_id = ? AND person_id = ?", sessionID, personID).
		Order("summary_version DESC").First(&n).Error
	if err != nil {
		return nil
	}
	return &n
}

// GetContext assembles bounded recent messages with summary and narrative context.
func GetContext(sessionID, personID, maxMessageID int64, recentCount int) *RetrievalResult {
	result := &RetrievalResult{
		RecentMessages:   []model.Message{},
		RelevantSegments: []comprehend.Segment{},
	}

	result.RecentMessages = getRecentMessagesBefore(sessionID, maxMessageID, recentCount)

	result.SummaryVersion, result.Narrative = buildSummaryAndNarrative(sessionID, personID)

	return result
}

func getRecentMessagesBefore(sessionID, maxMessageID int64, limit int) []model.Message {
	query := database.DB.Where("session_id = ?", sessionID)
	if maxMessageID > 0 {
		query = query.Where("id <= ?", maxMessageID)
	}
	var messages []model.Message
	if err := query.Order("id DESC").Limit(limit).Find(&messages).Error; err != nil {
		applogger.Error("failed to load bounded recent messages", "session_id", sessionID, "max_message_id", maxMessageID, "error", err)
		return nil
	}
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	return messages
}
