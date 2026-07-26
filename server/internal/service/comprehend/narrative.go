package comprehend

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"

	applogger "qingqiu-world-server/internal/logger"
)

// narrativeManager coordinates per-(session, agent) narrative generation goroutines.
// The key is "sessionID-agentConfigID", so each agent in a session gets its own
// independent narrative generation path, decoupled from summary generation.
//
// Goroutines are tracked with cancellable contexts so they can be aborted
// when a session or agent is deleted.
type narrativeManager struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc // "sessionID-agentConfigID" → cancel func
}

var nm = &narrativeManager{
	running: make(map[string]context.CancelFunc),
}

// narrativeKey builds the unique key for the narrative manager map.
func narrativeKey(sessionID, personID int64) string {
	return fmt.Sprintf("%d-%d", sessionID, personID)
}

// SignalNarrative signals that the session-agent pair may need narrative generation.
// If a goroutine is already running for this (session, agent), the call is a no-op.
func SignalNarrative(sessionID, personID int64) {
	key := narrativeKey(sessionID, personID)
	nm.mu.Lock()
	if _, ok := nm.running[key]; ok {
		nm.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	nm.running[key] = cancel
	nm.mu.Unlock()

	go nm.run(ctx, sessionID, personID)
}

// CancelNarrativesForSession cancels all running narrative goroutines for a
// session. Called when a session is deleted to prevent orphaned DB writes.
func CancelNarrativesForSession(sessionID int64) {
	prefix := fmt.Sprintf("%d-", sessionID)
	nm.mu.Lock()
	for key, cancel := range nm.running {
		if strings.HasPrefix(key, prefix) {
			cancel()
		}
	}
	nm.mu.Unlock()
}

func (nm *narrativeManager) clearRunning(sessionID, personID int64) {
	key := narrativeKey(sessionID, personID)
	nm.mu.Lock()
	if cancel, ok := nm.running[key]; ok {
		cancel() // release context resources
		delete(nm.running, key)
	}
	nm.mu.Unlock()
}

// run decides whether to generate a narrative for the agent.
func (nm *narrativeManager) run(ctx context.Context, sessionID, personID int64) {
	defer nm.clearRunning(sessionID, personID)

	// Check cancellation before any work
	if ctx.Err() != nil {
		return
	}

	// Step 1: compute the target version
	latestNarrative := getLatestNarrativeByIDs(sessionID, personID)
	startSeq := 1
	if latestNarrative != nil {
		startSeq = latestNarrative.SummaryVersion + 1
	}

	targetVersion, _, _ := computeTargetVersion(sessionID, startSeq)
	if targetVersion == 0 {
		return
	}

	// Step 2: check summary at target version
	targetSummary := getSessionSummary(sessionID, targetVersion)
	if targetSummary == nil {
		SignalSummary(sessionID)
		return
	}

	// Step 3: generate narrative

	// Check cancellation before loading agent and making the LLM call
	if ctx.Err() != nil {
		return
	}

	var ac model.AgentConfig
	if err := database.DB.Where("person_id = ?", personID).First(&ac).Error; err != nil {
		applogger.Error("SignalNarrative: agent config not found", "person_id", personID, "error", err)
		return
	}
	var llmConfig model.LLMConfig
	if err := database.DB.First(&llmConfig, ac.LLMConfigID).Error; err != nil {
		applogger.Error("SignalNarrative: LLM config not found", "config_id", ac.LLMConfigID, "error", err)
		return
	}

	// Check cancellation before the expensive LLM call
	if ctx.Err() != nil {
		return
	}

	narrativeContent := generateNarrativeFromSummary(ctx, &llmConfig, &ac, targetSummary.Content)
	if narrativeContent == "" {
		applogger.Error("SignalNarrative: narrative generation returned empty content",
			"session_id", sessionID, "person_id", personID)
		return
	}

	// Check cancellation before DB write
	if ctx.Err() != nil {
		return
	}

	narrative := model.AgentNarrative{
		SessionID:      sessionID,
		PersonID:       personID,
		SummaryVersion: targetSummary.Version,
		Content:        narrativeContent,
	}
	if err := database.DB.Create(&narrative).Error; err != nil {
		applogger.Error("SignalNarrative: failed to save narrative",
			"session_id", sessionID, "person_id", personID, "error", err)
		return
	}

	applogger.Info("Created agent narrative",
		"session_id", sessionID, "person_id", personID, "summary_version", targetSummary.Version,
		"length", len(narrativeContent))
}

// cachedNarrativePrompt generates a first-person experiential narrative from summary content.
// Used for cached narrative generation after summary creation.
//
// The agent's name and character settings are injected so the LLM generates the
// narrative from that specific agent's perspective — not a generic rephrasing.
const cachedNarrativePrompt = `You are %s, %s.

Rewrite the following conversation summary as a first-hand background narrative from YOUR perspective — as if you are recalling your own lived experience.

Requirements:
1. Write in first-person perspective. You ARE the person who lived through this conversation.
2. Preserve ALL key information from the summary
3. Transform the summary into a flowing, natural recollection
4. Do NOT add interpretations, judgments, or assumptions beyond what is stated
5. Maintain information fidelity

IMPORTANT: The narrative MUST preserve the original language of the conversation.
- If the conversation is in Chinese, write the narrative in Chinese.
- If the conversation is in English, write the narrative in English.
- If the conversation contains multiple languages, the narrative may also contain multiple languages.
- Do NOT translate between languages. Maintain information fidelity.

Conversation summary:
%s`

// generateNarrativeFromSummary generates a cached narrative from summary content,
// written from the perspective of the given agent.
//
// The agent's name and character settings are injected into the prompt so the
// resulting narrative reflects that specific agent's voice and identity.
func generateNarrativeFromSummary(ctx context.Context, llmConfig *model.LLMConfig, ac *model.AgentConfig, summaryContent string) string {
	if summaryContent == "" {
		return ""
	}

	prompt := fmt.Sprintf(cachedNarrativePrompt, dops.GetAgentConfigName(ac.ID), ac.CharacterSettings, summaryContent)

	chatModel := llm.NewChatModelWithTemperature(llmConfig.BaseURL, llmConfig.APIKey, llmConfig.ModelID, llm.TemperatureControlled)

	result, err := chatModel.Chat(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	})
	if err != nil {
		applogger.Error("Failed to generate cached narrative", "error", err)
		return ""
	}

	applogger.Info("Generated cached narrative from summary", "length", len(result))
	return result
}
