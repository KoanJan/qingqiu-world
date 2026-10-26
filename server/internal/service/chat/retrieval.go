package chat

import (
	"qingqiu-world-server/internal/model"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/memory"

	applogger "qingqiu-world-server/internal/logger"
)

// retrievalResult holds all context components retrieved for chat processing.
type retrievalResult struct {
	RecentMessages   []model.Message           `json:"recent_messages"`
	RelevantSegments []comprehendTypes.Segment `json:"relevant_segments"`
	SummaryVersion   int                       `json:"summary_version"`
	Narrative        string                    `json:"narrative"`
}

// getContext assembles bounded recent messages with summary and narrative context.
func getContext(sessionID, personID, maxMessageID int64, recentCount int) *retrievalResult {
	result := &retrievalResult{
		RecentMessages:   []model.Message{},
		RelevantSegments: []comprehendTypes.Segment{},
		SummaryVersion:   -1,
	}

	messages, version, narrative, err := memory.LoadObservedSessionContext(personID, sessionID, maxMessageID, recentCount, nil)
	if err != nil {
		applogger.Error("failed to load bounded chat context", "session_id", sessionID, "error", err)
		return result
	}
	result.RecentMessages = messages
	result.SummaryVersion = version
	result.Narrative = narrative
	return result
}
