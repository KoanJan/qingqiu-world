package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/llm"

	applogger "qingqiu-world-server/internal/logger"
)

// personStateInferencePrompt is the LLM prompt template for inferring the current state of the person you are talking to.
// It takes three parameters: agent_name, character_settings, recent_messages (formatted dialog text).
// The role context ensures the LLM correctly interprets the conversation as role-playing
// rather than treating casual questions (e.g., "Are you asleep?") as needing real-time information.
const personStateInferencePrompt = `You are %s, %s. You are inferring the current state of the person you are talking to.

Analyze their emotional tone, conversational purpose, and any clues about their physical situation.

Recent conversation:
%s`

// formatRecentMessages formats recent messages into text for the inference prompt.
// personName is the actual name of the other party (the partner being inferred),
// agentName is the agent's own name, selfPersonID is the agent's own person ID.
//
// Role labeling is keyed on the agent's own person ID (self), not on a hardcoded
// human user: the agent's own messages are labeled with agentName, every other
// participant's messages with personName. This keeps A2A sessions correct, where
// neither party is the human user.
func formatRecentMessages(recentMessages []model.Message, personName, agentName string, selfPersonID int64) string {
	var lines []string
	for _, msg := range recentMessages {
		role := personName
		if msg.PersonID == selfPersonID {
			role = agentName
		}
		lines = append(lines, fmt.Sprintf("%s [%s]: %s", role, msg.CreatedAt.Format("2006-01-02 15:04:05"), msg.Content))
	}
	return strings.Join(lines, "\n")
}

// InferPersonState infers the pserson's current state from recent conversation messages.
// Uses TemperatureDeterministic for consistent, deterministic outputs.
// personName is the actual name of the person being talked to (the partner),
// agentName is the agent's own name, selfPersonID is the agent's own person ID
// (used to label the agent's own messages in the dialog).
// characterSettings provides the agent's role context to prevent misinterpretation of casual questions.
// activeWorksSummary describes the agent's currently running works, enabling self-awareness
// (e.g., understanding "change the approach" refers to an ongoing task).
// Returns nil if inference fails, allowing the chat flow to continue without person state.
func InferPersonState(
	ctx context.Context,
	llmConfig *model.LLMConfig,
	recentMessages []model.Message,
	personName string,
	agentName string,
	selfPersonID int64,
	characterSettings string,
	activeWorksSummary string,
) *types.PersonState {
	if len(recentMessages) == 0 {
		return nil
	}

	chatModel := llm.NewChatModelWithTemperature(llmConfig.BaseURL, llmConfig.APIKey, llmConfig.ModelID, llm.TemperatureDeterministic)

	dialogText := formatRecentMessages(recentMessages, personName, agentName, selfPersonID)
	prompt := fmt.Sprintf(personStateInferencePrompt, agentName, characterSettings, dialogText)

	// Inject active works context for self-awareness.
	// When the agent knows what it is currently doing, it can correctly
	// interpret references like "change the approach" or "stop that".
	if activeWorksSummary != "" {
		prompt += "\n\n" + activeWorksSummary
	}

	result, err := chatModel.ChatWithJSONSchema(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, llm.JSONSchemaDefinition{
		Name:        "PersonState",
		Description: "Infer the person's current state from conversation context",
		Strict:      true,
		Schema:      llm.GenerateSchema[types.PersonState](),
	})

	if err != nil {
		applogger.Error("Failed to infer person state", "error", err)
		return nil
	}

	if result != "" {
		var state types.PersonState
		if err := json.Unmarshal([]byte(result), &state); err == nil {
			applogger.Info("Inferred person state",
				"emotion", state.Emotion,
				"purpose", state.Purpose,
				"situation", state.Situation,
			)
			return &state
		}
	}

	return nil
}
