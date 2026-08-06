package comprehend

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/kb"
)

// ComprehensionResult holds the outcome of the comprehension phase.
// It represents the agent's understanding of the incoming event,
// including what the other party means and what information is relevant.
//
// Comprehend only collects information — it does not make judgments.
type ComprehensionResult struct {
	ReadMessageRange [2]int64
	EventDescription string
	HistorySearch    *HistorySearch
	KBRetrieval      *KBRetrieval

	// NeedsClarification indicates the query is too vague and needs
	// a clarification question before proceeding.
	NeedsClarification bool

	// Clarification contains the generated clarification question
	// when NeedsClarification is true.
	Clarification string

	// PersonState holds the inferred state of the other party
	// (emotion, purpose, situation).
	PersonState *PersonState

	// ActiveWorksSummary is a natural language description of the agent's
	// currently running works. This gives the Comprehend phase self-awareness:
	// when the user says "change the approach" or "stop", the agent knows
	// what it is currently doing and can understand the reference.
	ActiveWorksSummary string
}

// HistorySearch describes a completed keyword search over conversation history.
type HistorySearch struct {
	Keywords []string
	Segments []Segment
}

// KBRetrieval describes a completed vector retrieval from configured knowledge bases.
type KBRetrieval struct {
	Query    string
	Segments []Segment
}

// ConversationMessage is a domain-level message used during comprehension.
type ConversationMessage struct {
	PersonID   int64
	PersonName string
	Content    string
	CreatedAt  time.Time
}

// SessionInfo holds session-level parameters needed for comprehension.
// These are loaded once from the database and passed to Comprehend
// to avoid repeated queries.
type SessionInfo struct {
	SessionID    int64
	MessageCount int64
	WindowSize   int
	KBIDs        []int64
	// PartnerName is the name of the conversation partner — the other
	// participant in this session, resolved from participant_sessions.
	// This is the actual person the agent is talking to (human in user-agent
	// sessions, another agent in A2A sessions), replacing a former hardcoded
	// human-user assumption that broke A2A addressing.
	PartnerName string
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
	if event.Type == eventqueue.EventTypeNewPrivateChatMessage {
		participantSession, err := dops.GetParticipantSession(event.SessionID, ac.PersonID)
		if err != nil {
			applogger.Error("Comprehend: failed to load participant session", "session_id", event.SessionID, "person_id", ac.PersonID, "error", err)
			return result
		}
		maxMessageID, err := dops.GetMaxMessageID(event.SessionID)
		if err != nil {
			applogger.Error("Comprehend: failed to load maximum message ID", "session_id", event.SessionID, "error", err)
			return result
		}
		result.ReadMessageRange = [2]int64{participantSession.LastReadMessageID, maxMessageID}
		messages, err := dops.ListMessagesInRange(event.SessionID, result.ReadMessageRange[0], result.ReadMessageRange[1])
		if err != nil {
			applogger.Error("Comprehend: failed to load message range", "session_id", event.SessionID, "error", err)
			return result
		}
		eventDescription = formatMessageRange(messages)
	}
	if eventDescription == "" {
		applogger.Info("Comprehend: empty event, skipping",
			"person_id", ac.PersonID,
			"session_id", sessionInfo.SessionID,
		)
		return result
	}

	result.EventDescription = eventDescription

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
			)
			result.NeedsClarification = preprocessingResult.NeedsClarification
			result.Clarification = preprocessingResult.Clarification
			if len(preprocessingResult.HistorySearchKeywords) > 0 {
				result.HistorySearch = &HistorySearch{
					Keywords: preprocessingResult.HistorySearchKeywords,
					Segments: SearchMessagesByKeywordsBefore(
						[]int64{event.SessionID},
						result.ReadMessageRange[1],
						preprocessingResult.HistorySearchKeywords,
						defaultHistorySearchLimit,
					),
				}
			}

			// Step 3: Knowledge base retrieval
			if len(sessionInfo.KBIDs) > 0 && preprocessingResult.KnowledgeBaseQuery != "" {
				result.KBRetrieval = &KBRetrieval{Query: preprocessingResult.KnowledgeBaseQuery}
				kbResults, err := kb.SearchMultiKB(ctx, sessionInfo.KBIDs, result.KBRetrieval.Query, kb.DefaultSearchTopK)
				if err != nil {
					applogger.Error("Comprehend: KB retrieval failed",
						"session_id", sessionInfo.SessionID,
						"error", err,
					)
				} else {
					for _, kr := range kbResults {
						result.KBRetrieval.Segments = append(result.KBRetrieval.Segments, Segment{
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
		recentMessagesForState := getRecentMessagesBefore(
			sessionInfo.SessionID,
			result.ReadMessageRange[1],
			min(int(sessionInfo.MessageCount), sessionInfo.WindowSize),
		)
		result.PersonState = InferPersonState(
			ctx,
			llmConfig,
			recentMessagesForState,
			sessionInfo.PartnerName,
			dops.GetAgentConfigName(ac.ID),
			ac.PersonID,
			ac.CharacterSettings,
			result.ActiveWorksSummary,
		)
	})

	// waiting for 3 steps finish
	wg.Wait()

	applogger.Info("Comprehend completed",
		"agent_config_id", ac.ID,
		"session_id", sessionInfo.SessionID,
		"needs_clarification", result.NeedsClarification,
		"history_segments", historySegmentCount(result.HistorySearch),
		"kb_segments", kbSegmentCount(result.KBRetrieval),
	)

	return result
}

func getRecentMessagesBefore(sessionID, maxMessageID int64, limit int) []model.Message {
	query := database.DB.Where("session_id = ?", sessionID)
	if maxMessageID > 0 {
		query = query.Where("id <= ?", maxMessageID)
	}
	var messages []model.Message
	if err := query.Order("id DESC").Limit(limit).Find(&messages).Error; err != nil {
		applogger.Error("Comprehend: failed to load bounded recent messages", "session_id", sessionID, "max_message_id", maxMessageID, "error", err)
		return nil
	}
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	return messages
}

func historySegmentCount(search *HistorySearch) int {
	if search == nil {
		return 0
	}
	return len(search.Segments)
}

func kbSegmentCount(retrieval *KBRetrieval) int {
	if retrieval == nil {
		return 0
	}
	return len(retrieval.Segments)
}

func formatMessageRange(messages []model.Message) string {
	if len(messages) == 0 {
		return ""
	}
	personIDs := make([]int64, 0, len(messages))
	seenPersonIDs := make(map[int64]struct{}, len(messages))
	for _, message := range messages {
		if _, exists := seenPersonIDs[message.PersonID]; !exists {
			seenPersonIDs[message.PersonID] = struct{}{}
			personIDs = append(personIDs, message.PersonID)
		}
	}
	names, err := dops.GetPersonNames(personIDs)
	if err != nil {
		applogger.Error("Comprehend: failed to load message sender names", "error", err)
		names = map[int64]string{}
	}
	lines := make([]string, 0, len(messages)+1)
	lines = append(lines, "[Private chat]")
	for _, message := range messages {
		name := names[message.PersonID]
		if name == "" {
			name = fmt.Sprintf("person_%d", message.PersonID)
		}
		lines = append(lines, fmt.Sprintf("%s [%s]: %s", name, message.CreatedAt.Format("2006-01-02 15:04:05"), message.Content))
	}
	return strings.Join(lines, "\n")
}

// getPreprocessingHistory retrieves recent messages for preprocessing context.
// Returns messages as llm.Message slice in chronological order.
// This is the same logic that was in chat.getPreprocessingHistory.
func getPreprocessingHistory(sessionID int64, limit int) []ConversationMessage {
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

	personIDs := make([]int64, 0, len(messages))
	seenPersonIDs := make(map[int64]struct{}, len(messages))
	for _, message := range messages {
		if _, exists := seenPersonIDs[message.PersonID]; !exists {
			seenPersonIDs[message.PersonID] = struct{}{}
			personIDs = append(personIDs, message.PersonID)
		}
	}
	names, err := dops.GetPersonNames(personIDs)
	if err != nil {
		applogger.Error("getPreprocessingHistory: failed to load person names", "error", err)
		names = map[int64]string{}
	}

	history := make([]ConversationMessage, 0, len(messages))
	for _, message := range messages {
		personName := names[message.PersonID]
		if personName == "" {
			personName = fmt.Sprintf("person_%d", message.PersonID)
		}
		history = append(history, ConversationMessage{
			PersonID:   message.PersonID,
			PersonName: personName,
			Content:    message.Content,
			CreatedAt:  message.CreatedAt,
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
	}

	// Resolve the conversation partner — the other participant in this
	// session — so person-state inference describes the actual partner
	// (human in user-agent sessions, another agent in A2A sessions) rather
	// than a hardcoded human user.
	if partner, err := dops.GetSessionOtherParticipant(sessionID, ac.PersonID); err != nil {
		applogger.Error("buildSessionInfo: failed to resolve session partner",
			"session_id", sessionID, "self_person_id", ac.PersonID, "error", err)
	} else if partner != nil {
		info.PartnerName = partner.Name
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
