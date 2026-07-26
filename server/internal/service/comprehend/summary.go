package comprehend

import (
	"context"
	"fmt"
	"sync"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"

	applogger "qingqiu-world-server/internal/logger"
)

// summaryManager coordinates per-session summary generation goroutines.
// It ensures at most one goroutine per session is active at any time,
// avoiding redundant concurrent summary generation.
//
// Goroutines are tracked with cancellable contexts so they can be aborted
// when a session is deleted — preventing writes to orphaned DB rows.
type summaryManager struct {
	mu      sync.Mutex
	running map[int64]context.CancelFunc // sessionID → cancel func
}

var sm = &summaryManager{
	running: make(map[int64]context.CancelFunc),
}

// SignalSummary signals that the session may need summary generation.
// If a goroutine is already running for this session, the call is a no-op.
func SignalSummary(sessionID int64) {
	sm.mu.Lock()
	if _, ok := sm.running[sessionID]; ok {
		sm.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	sm.running[sessionID] = cancel
	sm.mu.Unlock()

	go sm.run(ctx, sessionID)
}

// CancelSummary cancels any running summary goroutine for the session.
// Called when a session is deleted to prevent orphaned DB writes.
func CancelSummary(sessionID int64) {
	sm.mu.Lock()
	if cancel, ok := sm.running[sessionID]; ok {
		cancel()
	}
	sm.mu.Unlock()
}

func (sm *summaryManager) clearRunning(sessionID int64) {
	sm.mu.Lock()
	if cancel, ok := sm.running[sessionID]; ok {
		cancel() // release context resources
		delete(sm.running, sessionID)
	}
	sm.mu.Unlock()
}

// run performs a single summary generation cycle for the session.
func (sm *summaryManager) run(ctx context.Context, sessionID int64) {
	defer sm.clearRunning(sessionID)

	// Check cancellation before any work
	if ctx.Err() != nil {
		return
	}

	llmConfig := dops.GetSystemLLMConfig()
	if llmConfig == nil {
		applogger.Error("SignalSummary: system LLM not configured")
		return
	}

	// Determine where to start reading messages
	latestSummary := getLatestSummaryByID(sessionID)
	startSeq := 1
	if latestSummary != nil {
		startSeq = latestSummary.Version + 1
	}

	endSeq, msgCount, tokenCount := computeTargetVersion(sessionID, startSeq)
	if endSeq == 0 {
		return
	}

	// Check cancellation before the expensive LLM call
	if ctx.Err() != nil {
		return
	}

	applogger.Info("SignalSummary: generating summary",
		"session_id", sessionID, "start_seq", startSeq, "end_seq", endSeq,
		"msg_count", msgCount, "token_count", tokenCount,
	)

	if err := generateSummaryRange(ctx, sessionID, llmConfig, startSeq, endSeq); err != nil {
		applogger.Error("SignalSummary: summary generation failed",
			"session_id", sessionID, "error", err)
	}
}

// summaryPrompt is the LLM prompt template for conversation summarization.
// It takes two parameters: baseline_summary and recent_messages.
const summaryPrompt = `Generate a summary based on the conversation history and baseline summary.

Baseline summary (if exists):
%s

Recent conversation:
%s

Generate a concise but complete summary that includes key information, decisions, and context from the conversation. The summary should help understand the background for subsequent conversations.

IMPORTANT: The summary MUST preserve the original language of the conversation.
- If the conversation is in Chinese, write the summary in Chinese.
- If the conversation is in English, write the summary in English.
- If the conversation contains multiple languages, the summary may also contain multiple languages.
- Do NOT translate between languages. Maintain information fidelity.`

// generateSummaryRange generates a summary covering messages startSeq..endSeq.
//
// If startSeq == 1 (no prior summary exists), a full summary is generated from
// all covered messages. Otherwise, the summary at version (startSeq-1) is used
// as baseline, and only the new messages are incrementally summarized.
//
// The result is stored at version = endSeq. No recursive baseline generation
// — the caller is responsible for ensuring the baseline summary exists.
func generateSummaryRange(ctx context.Context, sessionID int64, llmConfig *model.LLMConfig, startSeq, endSeq int) error {
	existing := getSessionSummary(sessionID, endSeq)
	if existing != nil {
		applogger.Info("Summary already exists", "session_id", sessionID, "version", endSeq)
		return nil
	}

	var prompt string

	if startSeq == 1 {
		// Full summary: no baseline exists, summarize all messages 1..endSeq
		messages := getMessagesByRange(sessionID, 1, endSeq)
		if len(messages) == 0 {
			applogger.Error("No messages found for summary", "session_id", sessionID, "range", fmt.Sprintf("1-%d", endSeq))
			return nil
		}
		messagesText := formatMessagesForSummaryGeneric(messages)
		prompt = fmt.Sprintf(summaryPrompt, "(No baseline summary, this is the first summary)", messagesText)
	} else {
		// Incremental summary: baseline = summary at (startSeq-1), recent = startSeq..endSeq
		baseline := getSessionSummary(sessionID, startSeq-1)
		if baseline == nil {
			applogger.Error("Baseline summary not found", "session_id", sessionID, "baseline_version", startSeq-1)
			return nil
		}
		messages := getMessagesByRange(sessionID, startSeq, endSeq)
		if len(messages) == 0 {
			applogger.Error("No messages found for summary", "session_id", sessionID, "range", fmt.Sprintf("%d-%d", startSeq, endSeq))
			return nil
		}
		messagesText := formatMessagesForSummaryGeneric(messages)
		prompt = fmt.Sprintf(summaryPrompt, baseline.Content, messagesText)
	}

	chatModel := llm.NewChatModelWithTemperature(llmConfig.BaseURL, llmConfig.APIKey, llmConfig.ModelID, llm.TemperatureCreative)
	summaryContent, err := chatModel.Chat(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	})
	if err != nil {
		applogger.Error("Summary generation LLM call failed", "session_id", sessionID, "error", err)
		return fmt.Errorf("summary generation failed: %w", err)
	}

	applogger.Info("Generated summary content", "session_id", sessionID, "version", endSeq)

	newSummary := model.Summary{
		SessionID: sessionID,
		Version:   endSeq,
		Content:   summaryContent,
	}
	if err := database.DB.Create(&newSummary).Error; err != nil {
		return err
	}

	applogger.Info("Created session summary", "session_id", sessionID, "version", endSeq)
	return nil
}

// getSessionSummary retrieves a specific session-level summary by (session_id, version).
func getSessionSummary(sessionID int64, version int) *model.Summary {
	var s model.Summary
	err := database.DB.Where("session_id = ? AND version = ?", sessionID, version).First(&s).Error
	if err != nil {
		return nil
	}
	return &s
}

// getLatestSummaryByID returns the latest summary for a session.
func getLatestSummaryByID(sessionID int64) *model.Summary {
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

// getAgentNarrative retrieves a specific narrative by (session_id, agent_config_id, summary_version).
func getAgentNarrative(sessionID, personID int64, summaryVersion int) *model.AgentNarrative {
	var n model.AgentNarrative
	err := database.DB.Where("session_id = ? AND person_id = ? AND summary_version = ?",
		sessionID, personID, summaryVersion).First(&n).Error
	if err != nil {
		return nil
	}
	return &n
}

// getMessagesByRange returns messages by session-internal sequence numbers (1-based, inclusive).
// Messages are ordered by their global ID, which corresponds to their insertion order.
func getMessagesByRange(sessionID int64, startSeq, endSeq int) []*model.Message {
	var messages []*model.Message
	if err := database.DB.Where("session_id = ?", sessionID).
		Order("id ASC").
		Offset(startSeq - 1).
		Limit(endSeq - startSeq + 1).
		Find(&messages).Error; err != nil {
		applogger.Error("getMessagesByRange: failed to load messages", "session_id", sessionID, "error", err)
		return nil
	}
	return messages
}

// formatMessagesForSummary formats messages for the summary prompt.
// Converts message objects into a human-readable format suitable for LLM summarization.
// userName is the actual name of the other party, agentName is the agent's own name.
// Kept for backward compatibility with narrative generation which needs named roles.
func formatMessagesForSummary(messages []model.Message, personName, agentName string) string {
	userPersonID, err := dops.GetCurrentUserPersonID()
	if err != nil {
		applogger.Error("formatMessagesForSummary: failed to get current user person ID", "error", err)
	}
	personRole := personName
	var formatted []string
	for _, msg := range messages {
		role := personRole
		if userPersonID != 0 && msg.PersonID != userPersonID {
			role = agentName
		}
		formatted = append(formatted, fmt.Sprintf("%s: %s", role, msg.Content))
	}
	result := ""
	for i, s := range formatted {
		if i > 0 {
			result += "\n\n"
		}
		result += s
	}
	return result
}

// formatMessagesForSummaryGeneric formats messages using role-based labels
// (User/Assistant) suitable for a session-level factual summary.
func formatMessagesForSummaryGeneric(messages []*model.Message) string {
	userPersonID, err := dops.GetCurrentUserPersonID()
	if err != nil {
		applogger.Error("formatMessagesForSummaryGeneric: failed to get current user person ID", "error", err)
	}
	userName := dops.GetUserName()
	var formatted []string
	for _, msg := range messages {
		role := userName
		if userPersonID != 0 && msg.PersonID != userPersonID {
			role = "Assistant"
		}
		formatted = append(formatted, fmt.Sprintf("%s: %s", role, msg.Content))
	}
	result := ""
	for i, s := range formatted {
		if i > 0 {
			result += "\n\n"
		}
		result += s
	}
	return result
}
