package chatcontext

import (
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/comprehend"

	applogger "qingqiu-world-server/internal/logger"
)

// DefaultKeywordMatchCount is the default number of keyword-matched segments retrieved from chat history.
const DefaultKeywordMatchCount = 5

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

// GetContextWithoutRetrieval retrieves context without keyword retrieval.
// Used for queries that don't need retrieval (e.g., greetings, chitchat).
// Retrieves recent messages, latest summary, and cached narrative.
func GetContextWithoutRetrieval(sessionID, personID int64, recentCount int) *RetrievalResult {
	result := &RetrievalResult{
		RecentMessages:   []model.Message{},
		RelevantSegments: []comprehend.Segment{},
	}

	result.RecentMessages = comprehend.GetRecentMessages(sessionID, recentCount)

	result.SummaryVersion, result.Narrative = buildSummaryAndNarrative(sessionID, personID)

	return result
}

// GetContextForChat retrieves context for chat response generation using
// keyword-based search on session history messages.
// Returns:
//  1. Recent messages from the session
//  2. Keyword-matched segments from session history
//  3. Latest summary (if available)
//  4. Cached narrative from agent_narratives (if available)
func GetContextForChat(sessionID, personID int64, keywords []string, recentCount int, keywordMatchCount int) *RetrievalResult {
	result := &RetrievalResult{
		RecentMessages:   []model.Message{},
		RelevantSegments: []comprehend.Segment{},
	}

	result.RecentMessages = comprehend.GetRecentMessages(sessionID, recentCount)

	// Keyword-based retrieval: search messages in this session
	if len(keywords) > 0 {
		result.RelevantSegments = SearchMessagesByKeywords([]int64{sessionID}, keywords, keywordMatchCount)
		applogger.Info("Keyword retrieval completed",
			"session_id", sessionID,
			"keywords", keywords,
			"segment_count", len(result.RelevantSegments),
		)
	}

	latestSummary := getLatestSummaryBySessionID(sessionID)
	if latestSummary != nil {
		result.SummaryVersion = latestSummary.Version
	}
	latestNarrative := getLatestNarrativeByIDs(sessionID, personID)
	if latestNarrative != nil {
		result.Narrative = latestNarrative.Content
	}

	return result
}
