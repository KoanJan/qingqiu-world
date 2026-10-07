// Package chat implements the comprehension logic for private chat message
// events (EventTypeNewPrivateChatMessage).
//
// It is one event-type-specific sub-package of the comprehend router. The
// parent comprehend package owns routing and the public API; external callers
// should use comprehend, not this package directly.
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/kb"
	"qingqiu-world-server/internal/service/memory"
)

const comprehensionLocalContextLimit = 5

// ComprehendMessage performs the comprehension phase for a private chat
// message event: understanding what the other party means before making any
// judgment.
//
// It returns the formatted event description plus the chat-specific
// comprehension payload. Retrieval planning and person-state inference run
// here; the need to ask a question is decided later in Decide.
func ComprehendMessage(
	ctx context.Context,
	event *eventqueue.AgentEvent,
	ac *model.AgentConfig,
	llmConfig *model.LLMConfig,
	activeWorksSummary string,
) (string, *types.ChatComprehension) {
	sessionInfo := buildSessionInfo(event.SessionID, ac)

	result := &types.ChatComprehension{}

	participantSession, err := dops.GetParticipantSession(event.SessionID, ac.PersonID)
	if err != nil {
		applogger.Error("chat.ComprehendMessage: failed to load participant session", "session_id", event.SessionID, "person_id", ac.PersonID, "error", err)
		return "", result
	}
	maxMessageID, err := dops.GetMaxMessageID(event.SessionID)
	if err != nil {
		applogger.Error("chat.ComprehendMessage: failed to load maximum message ID", "session_id", event.SessionID, "error", err)
		return "", result
	}
	result.ReadMessageRange = [2]int64{participantSession.LastReadMessageID, maxMessageID}
	messages, err := dops.ListMessagesInRange(event.SessionID, result.ReadMessageRange[0], result.ReadMessageRange[1])
	if err != nil {
		applogger.Error("chat.ComprehendMessage: failed to load message range", "session_id", event.SessionID, "error", err)
		return "", result
	}
	for _, message := range messages {
		result.ReadMessageIDs = append(result.ReadMessageIDs, message.ID)
	}
	eventDescription := formatMessageRange(messages)
	if eventDescription == "" {
		applogger.Info("chat.ComprehendMessage: empty event, skipping",
			"person_id", ac.PersonID,
			"session_id", sessionInfo.SessionID,
		)
		return "", result
	}
	// Keep the same observed, bounded conversation window used by Chat. The
	// current batch is admitted explicitly because its observations are written
	// only after Comprehend returns.
	contextMessages, _, narrative, err := memory.LoadObservedSessionContext(
		ac.PersonID, event.SessionID, result.ReadMessageRange[1], sessionInfo.WindowSize, result.ReadMessageIDs,
	)
	if err != nil {
		applogger.Error("chat.ComprehendMessage: failed to load session context", "session_id", event.SessionID, "person_id", ac.PersonID, "error", err)
	}
	if sessionInfo.MessageCount >= int64(sessionInfo.WindowSize) {
		result.Narrative = narrative
	}
	result.RecentMessages = conversationMessagesFromModels(contextMessages, ac.PersonID)
	localMessages := contextMessages[max(0, len(contextMessages)-comprehensionLocalContextLimit):]
	localHistory := result.RecentMessages[max(0, len(result.RecentMessages)-comprehensionLocalContextLimit):]

	// concurrent work
	wg := sync.WaitGroup{}

	if sessionInfo.MessageCount >= int64(sessionInfo.WindowSize) || len(sessionInfo.AuthorizedKBs) > 0 {
		wg.Go(func() {
			// Step 1: Query preprocessing (conditional — same conditions as before)
			// Runs when V >= N (for context engineering) or when the agent holds
			// KB grants (for agentic KB retrieval decisions).
			preprocessingResult := PreprocessQuery(
				ctx,
				llmConfig,
				eventDescription,
				localHistory,
				sessionInfo.AuthorizedKBs,
				sessionInfo.WindowSize,
			)
			if len(preprocessingResult.HistorySearchKeywords) > 0 {
				// Chat fetches the matching history after its target session is known.
				result.HistorySearch = &types.HistorySearch{Keywords: preprocessingResult.HistorySearchKeywords}
			}

			// Step 3: Knowledge-base intent capture. Comprehend no longer runs
			// KB retrieval directly; complex KB use belongs to Focus, where the
			// agent can call scan_kb/read_kb_evidence iteratively.
			if len(sessionInfo.AuthorizedKBs) > 0 && preprocessingResult.KnowledgeBaseQuery != "" {
				selectedKBIDs := filterAuthorizedKBIDs(preprocessingResult.KnowledgeBaseIDs, sessionInfo.AuthorizedKBs)
				if len(selectedKBIDs) > 0 {
					result.KBRetrieval = &types.KBRetrieval{Query: preprocessingResult.KnowledgeBaseQuery, KnowledgeBaseIDs: selectedKBIDs}
					applogger.Info("chat.ComprehendMessage: KB investigation suggested",
						"session_id", sessionInfo.SessionID,
						"kb_ids", selectedKBIDs,
						"query_fingerprint", kb.QueryFingerprint(result.KBRetrieval.Query),
					)
				}
			}
		})
	}

	// Step 2: Person state inference (always runs — same as before)
	wg.Go(func() {
		result.PersonState = InferPersonState(
			ctx,
			llmConfig,
			localMessages,
			sessionInfo.PartnerName,
			dops.GetAgentConfigName(ac.ID),
			ac.PersonID,
			ac.CharacterSettings,
			activeWorksSummary,
		)
	})

	// waiting for 3 steps finish
	wg.Wait()

	applogger.Info("chat.ComprehendMessage completed",
		"agent_config_id", ac.ID,
		"session_id", sessionInfo.SessionID,
		"history_keywords", historyKeywordCount(result.HistorySearch),
		"kb_segments", kbSegmentCount(result.KBRetrieval),
	)

	return eventDescription, result
}

// filterAuthorizedKBIDs keeps only the KB IDs that appear in the authorized
// set. The routing prompt already restricts the LLM's choices, but this is
// defense in depth: out-of-scope IDs are dropped with a warning instead of
// being searched.
func filterAuthorizedKBIDs(selected []int64, authorized []types.KBDescriptor) []int64 {
	if len(selected) == 0 {
		return nil
	}
	authorizedSet := make(map[int64]struct{}, len(authorized))
	for _, item := range authorized {
		authorizedSet[item.ID] = struct{}{}
	}
	kept := make([]int64, 0, len(selected))
	for _, id := range selected {
		if _, ok := authorizedSet[id]; !ok {
			applogger.Warn("chat.filterAuthorizedKBIDs: dropping unauthorized KB ID from preprocessing decision", "kb_id", id)
			continue
		}
		kept = append(kept, id)
	}
	return kept
}

// historyKeywordCount reports how many search terms Comprehend planned.
func historyKeywordCount(search *types.HistorySearch) int {
	if search == nil {
		return 0
	}
	return len(search.Keywords)
}

// kbSegmentCount reports how many knowledge-base excerpts were retrieved.
func kbSegmentCount(retrieval *types.KBRetrieval) int {
	if retrieval == nil {
		return 0
	}
	return len(retrieval.Segments)
}

// formatMessageRange describes the unread message batch as one chat Event.
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
		applogger.Error("chat.formatMessageRange: failed to load message sender names", "error", err)
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

// conversationMessagesFromModels keeps observed speech and the agent's own
// recorded intention together when sharing the bounded chat window with Decide.
func conversationMessagesFromModels(messages []model.Message, selfPersonID int64) []types.ConversationMessage {
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
		applogger.Error("chat.conversationMessagesFromModels: failed to load person names", "error", err)
		names = map[int64]string{}
	}
	ownActions := loadOwnMessageActions(messages, selfPersonID)

	history := make([]types.ConversationMessage, 0, len(messages))
	for _, message := range messages {
		personName := names[message.PersonID]
		if personName == "" {
			personName = fmt.Sprintf("person_%d", message.PersonID)
		}
		history = append(history, types.ConversationMessage{
			ID:         message.ID,
			PersonID:   message.PersonID,
			PersonName: personName,
			Content:    message.Content,
			CreatedAt:  message.CreatedAt,
			OwnAction:  ownActions[message.ID],
		})
	}
	return history
}

// loadOwnMessageActions resolves only the current agent's outgoing messages.
// ActionEffect is the recorded Message-to-Action link; other speakers' private
// actions are never queried or exposed through conversation history.
func loadOwnMessageActions(messages []model.Message, selfPersonID int64) map[int64]*types.MessageActionContext {
	result := make(map[int64]*types.MessageActionContext)
	var ownMessageIDs []int64
	for _, message := range messages {
		if message.PersonID == selfPersonID {
			ownMessageIDs = append(ownMessageIDs, message.ID)
		}
	}
	if len(ownMessageIDs) == 0 {
		return result
	}
	var rows []struct {
		MessageID  int64  `gorm:"column:message_id"`
		Background string `gorm:"column:background"`
		Reason     string `gorm:"column:reason"`
		PlanJSON   string `gorm:"column:plan_json"`
	}
	err := database.DB.Table("action_effects AS effects").
		Select("effects.effect_id AS message_id, actions.background, actions.reason, actions.plan_json").
		Joins("JOIN actions ON actions.id = effects.action_id").
		Joins("JOIN decisions ON decisions.id = actions.decision_id").
		Where("effects.effect_type = ? AND effects.effect_id IN ? AND actions.type = ? AND decisions.person_id = ?",
			model.ActionEffectMessage, ownMessageIDs, model.ActionTypeChat, selfPersonID).
		Scan(&rows).Error
	if err != nil {
		applogger.Error("chat.loadOwnMessageActions: failed to load own message origins", "person_id", selfPersonID, "error", err)
		return result
	}
	seenIDs := make(map[int64]struct{})
	duplicateIDs := make(map[int64]struct{})
	for _, row := range rows {
		if _, duplicate := duplicateIDs[row.MessageID]; duplicate {
			continue
		}
		if _, duplicate := seenIDs[row.MessageID]; duplicate {
			applogger.Error("chat.loadOwnMessageActions: multiple Chat actions produced one message", "person_id", selfPersonID, "message_id", row.MessageID)
			duplicateIDs[row.MessageID] = struct{}{}
			delete(result, row.MessageID)
			continue
		}
		seenIDs[row.MessageID] = struct{}{}
		var plan struct {
			Guidance string `json:"guidance"`
		}
		if err := json.Unmarshal([]byte(row.PlanJSON), &plan); err != nil {
			applogger.Error("chat.loadOwnMessageActions: invalid Chat action plan", "person_id", selfPersonID, "message_id", row.MessageID, "error", err)
			continue
		}
		result[row.MessageID] = &types.MessageActionContext{
			Background: row.Background,
			Reason:     row.Reason,
			Guidance:   plan.Guidance,
		}
	}
	return result
}

// buildSessionInfo loads session-level parameters needed for comprehension.
// This is called once per event in the event loop, before ComprehendMessage().
func buildSessionInfo(sessionID int64, ac *model.AgentConfig) *types.SessionInfo {
	info := &types.SessionInfo{
		SessionID:  sessionID,
		WindowSize: 50, // Default window size
	}

	// Resolve the conversation partner — the other participant in this
	// session — so person-state inference describes the actual partner
	// (human in user-agent sessions, another agent in A2A sessions) rather
	// than a hardcoded human user.
	if partner, err := dops.GetSessionOtherParticipant(sessionID, ac.PersonID); err != nil {
		applogger.Error("chat.buildSessionInfo: failed to resolve session partner",
			"session_id", sessionID, "self_person_id", ac.PersonID, "error", err)
	} else if partner != nil {
		info.PartnerName = partner.Name
	}

	// Get message count for this session
	var messageCount int64
	if err := database.DB.Model(&model.Message{}).
		Where("session_id = ?", sessionID).
		Count(&messageCount).Error; err != nil {
		applogger.Error("chat.buildSessionInfo: failed to count messages",
			"session_id", sessionID, "error", err,
		)
	}
	info.MessageCount = messageCount

	// Load the KBs this agent is granted to access (kb_access table). The
	// inventory is both the LLM's retrieval-decision input and the
	// enforcement set for search. On load failure, KB retrieval is disabled
	// for this turn (degraded, never silent).
	authorizedKBs, err := dops.ListAuthorizedKBs(ac.PersonID)
	if err != nil {
		applogger.Error("chat.buildSessionInfo: failed to load authorized KBs, KB retrieval disabled for this turn",
			"session_id", sessionID, "person_id", ac.PersonID, "error", err)
	}
	info.AuthorizedKBs = make([]types.KBDescriptor, 0, len(authorizedKBs))
	for _, kbEntity := range authorizedKBs {
		info.AuthorizedKBs = append(info.AuthorizedKBs, types.KBDescriptor{
			ID:          kbEntity.ID,
			Name:        kbEntity.Name,
			Description: kbEntity.Description,
		})
	}

	return info
}
