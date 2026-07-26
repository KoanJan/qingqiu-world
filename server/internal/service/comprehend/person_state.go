package comprehend

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"

	applogger "qingqiu-world-server/internal/logger"
)

// personStateInferencePrompt is the LLM prompt template for inferring the current state of the person you are talking to.
// It takes three parameters: agent_name, character_settings, recent_messages (formatted dialog text).
// The role context ensures the LLM correctly interprets the conversation as role-playing
// rather than treating casual questions (e.g., "Are you asleep?") as needing real-time information.
const personStateInferencePrompt = `You are %s, %s. You are inferring the current state of the person you are talking to.

Analyze their emotional tone, conversational purpose, and any clues about their physical situation.

Also determine if their request requires interaction with the external world:
- Set needs_world_interaction=true if the request requires performing actions beyond conversation: 
  using tools, accessing real-time information (news, weather, stock prices), 
  file operations (create, modify, delete files), code execution, web searches, 
  or any operation that affects the external world.
- Set needs_world_interaction=false if the request can be fulfilled by conversation alone: 
  answering questions, giving advice, explaining concepts, casual chat, 
  expressing opinions, or any response that only requires reasoning and language.

IMPORTANT: You are the person named above(%s). Questions directed at you (e.g., "Are you asleep?", "How are you?", "What do you think?") are casual chat and do NOT require world interaction. Only set needs_world_interaction=true when the person explicitly asks you to perform actions that require tools or external data.

Recent conversation:
%s`

// PersonState represents the inferred person state from conversation context.
//
// Three-dimensional model:
//   - Emotion: person's current emotional state (affects response tone)
//   - Purpose: person's current conversational goal (affects response content direction)
//   - Situation: person's physical context (affects response constraints)
//
// Intent type is implicitly derived from purpose + situation, not modeled separately.
//
// Field descriptions serve dual purpose:
//  1. Guide LLM structured output generation
//  2. Provide natural language fragments for prompt template assembly
type PersonState struct {
	Emotion               string `json:"emotion" jsonschema:"description=The person's current emotional state: calm for relaxed or neutral, anxious for worried or uneasy, frustrated for annoyed or impatient (e.g. repeated failed attempts), urgent for time-pressured or emergency, curious for inquisitive or exploratory,enum=calm,enum=anxious,enum=frustrated,enum=urgent,enum=curious,required"`
	Purpose               string `json:"purpose" jsonschema:"description=The person's current conversational goal: seek_help for needing a solution or fix, seek_advice for wanting recommendations or guidance, seek_confirmation for validating a decision or understanding, express_feeling for sharing emotions without expecting solutions, casual_chat for social or non-goal-oriented conversation,enum=seek_help,enum=seek_advice,enum=seek_confirmation,enum=express_feeling,enum=casual_chat,required"`
	Situation             string `json:"situation" jsonschema:"description=Brief natural language description of the person's physical context if inferable from the conversation, such as time of day, device, environment, or activity. Use unknown if not inferable. Examples: at work on desktop, late evening on mobile, in a meeting, commuting,required"`
	NeedsWorldInteraction bool   `json:"needs_world_interaction" jsonschema:"description=Whether the request requires actions beyond conversation: true if it needs tools, real-time information, file operations, or any operation affecting the external world; false if it can be fulfilled by conversation alone (reasoning, advice, explanations, casual chat),required"`
}

// emotionDescriptions maps emotion codes to natural language descriptions.
var emotionDescriptions = map[string]string{
	"calm":       "calm and relaxed",
	"anxious":    "anxious or worried",
	"frustrated": "frustrated or impatient",
	"urgent":     "under time pressure or in urgency",
	"curious":    "curious and exploratory",
}

// purposeDescriptions maps purpose codes to natural language descriptions.
var purposeDescriptions = map[string]string{
	"seek_help":         "seeking help with a problem",
	"seek_advice":       "looking for advice or recommendations",
	"seek_confirmation": "seeking confirmation or validation",
	"express_feeling":   "expressing feelings without expecting solutions",
	"casual_chat":       "engaging in casual conversation",
}

// ToNaturalLanguage converts the structured person state into a natural language description
// suitable for injection into the prompt's instruction area.
// personName is the actual name of the person (empty = no profile set).
func (ps *PersonState) ToNaturalLanguage(personName string) string {
	emotionDesc := ps.Emotion
	if desc, ok := emotionDescriptions[ps.Emotion]; ok {
		emotionDesc = desc
	}
	purposeDesc := ps.Purpose
	if desc, ok := purposeDescriptions[ps.Purpose]; ok {
		purposeDesc = desc
	}

	subject := personName

	parts := []string{
		fmt.Sprintf("%s appears %s", subject, emotionDesc),
		fmt.Sprintf("is %s", purposeDesc),
	}
	if ps.Situation != "" && ps.Situation != "unknown" {
		parts = append(parts, fmt.Sprintf("and is likely %s", ps.Situation))
	}
	if ps.NeedsWorldInteraction {
		parts = append(parts, "and needs to interact with the external world (tools, real-time data, or file operations)")
	}

	return strings.Join(parts, ", ") + "."
}

// formatRecentMessages formats recent messages into text for the inference prompt.
// personName is the actual name of the other party, agentName is the agent's own name.
func formatRecentMessages(recentMessages []model.Message, personName, agentName string) string {
	userPersonID, err := dops.GetCurrentUserPersonID()
	if err != nil {
		applogger.Error("formatRecentMessages: failed to get current user person ID", "error", err)
	}
	personRole := personName
	var lines []string
	for _, msg := range recentMessages {
		role := personRole
		if userPersonID != 0 && msg.PersonID != userPersonID {
			role = agentName
		}
		lines = append(lines, fmt.Sprintf("%s [%s]: %s", role, msg.CreatedAt.Format("2006-01-02 15:04:05"), msg.Content))
	}
	return strings.Join(lines, "\n")
}

// InferPersonState infers the pserson's current state from recent conversation messages.
// Uses TemperatureDeterministic for consistent, deterministic outputs.
// userName is the actual name of the person being talked to, agentName is the agent's own name.
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
	characterSettings string,
	activeWorksSummary string,
) *PersonState {
	if len(recentMessages) == 0 {
		return nil
	}

	chatModel := llm.NewChatModelWithTemperature(llmConfig.BaseURL, llmConfig.APIKey, llmConfig.ModelID, llm.TemperatureDeterministic)

	dialogText := formatRecentMessages(recentMessages, personName, agentName)
	prompt := fmt.Sprintf(personStateInferencePrompt, agentName, characterSettings, agentName, dialogText)

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
		Schema:      llm.GenerateSchema[PersonState](),
	})

	if err != nil {
		applogger.Error("Failed to infer person state", "error", err)
		return nil
	}

	if result != "" {
		var state PersonState
		if err := json.Unmarshal([]byte(result), &state); err == nil {
			applogger.Info("Inferred person state",
				"emotion", state.Emotion,
				"purpose", state.Purpose,
				"situation", state.Situation,
				"needs_world_interaction", state.NeedsWorldInteraction,
			)
			return &state
		}
	}

	return nil
}
