package comprehend

import (
	"context"
	"encoding/json"
	"sync"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/kb"
	"qingqiu-world-server/internal/service/llm"
)

// ComprehensionResult holds the outcome of the comprehension phase.
// It represents the agent's understanding of the incoming event,
// including what the other party means and what information is relevant.
//
// Comprehend only collects information — it does not make judgments.
// NeedsWorldInteraction is an objective property of the message
// ("does this message involve tools/external data?"), not a decision
// on how to respond.
type ComprehensionResult struct {
	// ProcessedQuery is the rewritten query for better retrieval.
	// Empty if preprocessing was skipped.
	ProcessedQuery string

	// QueryType is the classified query type from preprocessing
	// (clear, ambiguous, vague, no_query).
	QueryType string

	// NeedsWorldInteraction indicates whether the message requires
	// interaction with the external world (tools, real-time data,
	// file operations). This is an objective property of the message,
	// not a decision on how to respond.
	NeedsWorldInteraction bool

	// NeedsClarification indicates the query is too vague and needs
	// a clarification question before proceeding.
	NeedsClarification bool

	// Clarification contains the generated clarification question
	// when NeedsClarification is true.
	Clarification string

	// SkipRetrieval indicates that RAG retrieval should be skipped
	// (e.g., for greetings, chitchat).
	SkipRetrieval bool

	// PreprocessingResult holds the original preprocessing output,
	// preserved for downstream consumers (e.g., chat execution)
	// that need the full structured result.
	PreprocessingResult *PreprocessingResult

	// KBSegments holds knowledge base retrieval results.
	KBSegments []Segment

	// PersonState holds the inferred state of the other party
	// (emotion, purpose, situation).
	PersonState *PersonState

	// ActiveWorksSummary is a natural language description of the agent's
	// currently running works. This gives the Comprehend phase self-awareness:
	// when the user says "change the approach" or "stop", the agent knows
	// what it is currently doing and can understand the reference.
	ActiveWorksSummary string
}

// SessionInfo holds session-level parameters needed for comprehension.
// These are loaded once from the database and passed to Comprehend
// to avoid repeated queries.
type SessionInfo struct {
	SessionID    int64
	MessageCount int64
	WindowSize   int
	KBIDs        []int64
	UserName     string
}

// Comprehend performs the comprehension phase: understanding what the
// other party means before making any judgment.
//
// This function extracts the information-gathering logic that was previously
// inside chat.Process(), making it available to the Decide phase.
// Prompt semantics are preserved — the same LLM calls are made, just in
// a different order (before Decide instead of after).
//
// Parameters:
//   - ctx: context for cancellation
//   - event: the incoming event to comprehend
//   - agent: the agent receiving the event
//   - llmConfig: LLM configuration for comprehension calls
//   - activeWorks: the agent's currently running works (for self-awareness)
func Comprehend(
	ctx context.Context,
	event *eventqueue.AgentEvent,
	ac *model.AgentConfig,
	llmConfig *model.LLMConfig,
	activeWorksSummary string,
) *ComprehensionResult {
	sessionInfo := buildSessionInfo(event.SessionID, ac)

	result := &ComprehensionResult{
		// Active works summary for self-awareness.
		// This allows the agent to understand references like "change the approach"
		// or "stop" by knowing what it is currently doing.
		ActiveWorksSummary: activeWorksSummary,
	}

	eventDescription := event.FormatDescription()
	if eventDescription == "" {
		applogger.Info("Comprehend: empty event, skipping",
			"person_id", ac.PersonID,
			"session_id", sessionInfo.SessionID,
		)
		return result
	}

	// default set
	result.ProcessedQuery = eventDescription
	result.QueryType = "clear"

	// For non-message events (work completed, scheduled, session joined/left),
	// skip the full comprehension pipeline — there is no "other party" to
	// understand, no query to preprocess, no KB to retrieve from.
	// The event description carries all the context the Decide phase needs.
	if event.Type != eventqueue.EventTypeNewPrivateChatMessage {
		applogger.Info("Comprehend completed (non-message event, skipped pipeline)",
			"agent_config_id", ac.ID,
			"session_id", sessionInfo.SessionID,
			"event_type", event.Type,
		)
		return result
	}

	// concurrent work
	wg := sync.WaitGroup{}

	if sessionInfo.MessageCount >= int64(sessionInfo.WindowSize) || len(sessionInfo.KBIDs) > 0 {
		wg.Go(func() {
			// Step 1: Query preprocessing (conditional — same conditions as before)
			// Runs when V >= N (for context engineering) or when knowledge bases
			// are configured (for KB retrieval optimization).
			preprocessingHistory := getPreprocessingHistory(sessionInfo.SessionID, sessionInfo.WindowSize)
			preprocessingResult := PreprocessQuery(
				ctx,
				llmConfig,
				eventDescription,
				preprocessingHistory,
				ac.CharacterSettings,
				sessionInfo.WindowSize,
				sessionInfo.UserName,
				dops.GetAgentConfigName(ac.ID),
			)
			// Extract preprocessing results
			result.ProcessedQuery = preprocessingResult.ProcessedQuery
			result.QueryType = preprocessingResult.QueryType
			result.NeedsClarification = preprocessingResult.NeedsClarification
			result.Clarification = preprocessingResult.Clarification
			result.SkipRetrieval = preprocessingResult.SkipRetrieval
			result.PreprocessingResult = preprocessingResult

			// Step 3: Knowledge base retrieval (conditional — same as before)
			if len(sessionInfo.KBIDs) > 0 {
				// Wait for preprocessing to complete before using the processed query
				query := eventDescription
				if result.ProcessedQuery != "" {
					query = result.ProcessedQuery
				}

				kbResults, err := kb.SearchMultiKB(ctx, sessionInfo.KBIDs, query, kb.DefaultSearchTopK)
				if err != nil {
					applogger.Error("Comprehend: KB retrieval failed",
						"session_id", sessionInfo.SessionID,
						"error", err,
					)
				} else {
					for _, kr := range kbResults {
						result.KBSegments = append(result.KBSegments, Segment{
							Content: kr.Content,
							Source:  SourceKnowledgeBase,
						})
					}
					applogger.Info("Comprehend: KB retrieved segments",
						"session_id", sessionInfo.SessionID,
						"count", len(kbResults),
					)
				}
			}
		})
	}

	// Step 2: Person state inference (always runs — same as before)
	wg.Go(func() {
		recentMessagesForState := GetRecentMessages(
			sessionInfo.SessionID,
			min(int(sessionInfo.MessageCount), sessionInfo.WindowSize),
		)
		result.PersonState = InferPersonState(
			ctx,
			llmConfig,
			recentMessagesForState,
			sessionInfo.UserName,
			dops.GetAgentConfigName(ac.ID),
			ac.CharacterSettings,
			result.ActiveWorksSummary,
		)
		// Extract PersonState results
		if result.PersonState != nil {
			result.NeedsWorldInteraction = result.PersonState.NeedsWorldInteraction
		}

	})

	// waiting for 3 steps finish
	wg.Wait()

	applogger.Info("Comprehend completed",
		"agent_config_id", ac.ID,
		"session_id", sessionInfo.SessionID,
		"needs_world_interaction", result.NeedsWorldInteraction,
		"query_type", result.QueryType,
		"needs_clarification", result.NeedsClarification,
		"kb_segments", len(result.KBSegments),
	)

	return result
}

// getPreprocessingHistory retrieves recent messages for preprocessing context.
// Returns messages as llm.Message slice in chronological order.
// This is the same logic that was in chat.getPreprocessingHistory.
func getPreprocessingHistory(sessionID int64, limit int) []llm.Message {
	var messages []model.Message
	if err := database.DB.Where("session_id = ?", sessionID).
		Order("id DESC").Limit(limit).Find(&messages).Error; err != nil {
		applogger.Error("getPreprocessingHistory: failed to load messages",
			"session_id", sessionID, "error", err,
		)
		return nil
	}

	// Reverse to chronological order
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	userPersonID, err := dops.GetCurrentUserPersonID()
	if err != nil {
		applogger.Error("getPreprocessingHistory: failed to get current user person ID", "error", err)
	}

	history := make([]llm.Message, 0, len(messages))
	for _, msg := range messages {
		role := "user"
		if userPersonID != 0 && msg.PersonID != userPersonID {
			role = "assistant"
		}
		history = append(history, llm.Message{
			Role:    role,
			Content: msg.Content,
		})
	}
	return history
}

// buildSessionInfo loads session-level parameters needed for comprehension.
// This is called once per event in the event loop, before Comprehend().
func buildSessionInfo(sessionID int64, ac *model.AgentConfig) *SessionInfo {
	info := &SessionInfo{
		SessionID:  sessionID,
		WindowSize: 50, // Default window size
		UserName:   dops.GetUserName(),
	}

	// Get message count for this session
	var messageCount int64
	if err := database.DB.Model(&model.Message{}).
		Where("session_id = ?", sessionID).
		Count(&messageCount).Error; err != nil {
		applogger.Error("buildSessionInfo: failed to count messages",
			"session_id", sessionID, "error", err,
		)
	}
	info.MessageCount = messageCount

	// Get knowledge base IDs for this agent config
	if ac.KnowledgeBaseIDs != "" && ac.KnowledgeBaseIDs != "[]" {
		var ids []int64
		if err := json.Unmarshal([]byte(ac.KnowledgeBaseIDs), &ids); err == nil {
			var validIDs []int64
			for _, id := range ids {
				var kb model.KnowledgeBase
				if err := database.DB.First(&kb, id).Error; err == nil {
					validIDs = append(validIDs, id)
				}
			}
			info.KBIDs = validIDs
		}
	}

	return info
}
