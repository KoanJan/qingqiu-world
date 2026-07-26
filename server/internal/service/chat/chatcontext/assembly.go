// Package context implements the context engineering pipeline for chat processing.
//
// This package provides the context assembly services that build the LLM message
// sequence from various context sources: summaries, narratives, retrieval results,
// person state, and task results. It matches Python's chat/context module.
package chatcontext

import (
	"fmt"
	"strings"

	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/comprehend"
	"qingqiu-world-server/internal/service/llm"

	applogger "qingqiu-world-server/internal/logger"
)

// Template for full context with background story, segments, and character settings.
// Uses narrative-style section headers instead of bracketed labels:
//   - "Background context from earlier" and "Recent conversation" create temporal flow
//   - Segments section uses narrative transition to reduce abruptness
//   - Metadata (message numbers) preserved for debugging and context clarity
//   - User state placed in instruction area (after narrative, before response directive)
//     to preserve narrative flow while guiding response strategy
//   - Guidance (from Decide phase) placed after person state, before response directive,
//     representing the agent's self-instruction on what to do
const oneBigMessageTemplate = `%s%sBackground context from earlier in the conversation:

%s

Recent conversation:

%s

---

%s%s%s%sRespond to the person you are talking to. Use the same language as the conversation. Do not use parenthetical action descriptions or non-verbal content.`

// Template for simple context without background story (V < N case).
// Used when there are not enough messages to generate a summary.
// Segments section is included when KB-retrieved content is available.
const oneBigMessageNoStoryTemplate = `%s%sConversation record:

%s

---

%s%s%s%sRespond to the person you are talking to. Use the same language as the conversation. Do not use parenthetical action descriptions or non-verbal content.`

// TaskResultForAssembly represents the task execution result for context assembly.
// Mirrors Python's TaskResult DTO used in context assembly.
// Status is "success" or "failure"; Result/Reason/Notes are populated accordingly.
type TaskResultForAssembly struct {
	Status string `json:"status"`
	Result string `json:"result"`
	Reason string `json:"reason"`
	Notes  string `json:"notes"`
}

// formatCharacterSection formats character settings section for the prompt.
// Returns "[Your Character]\n{settings}\n\n---\n\n" or empty string if nil/empty.
func formatCharacterSection(characterSettings string) string {
	if characterSettings == "" {
		return ""
	}
	return fmt.Sprintf("[Your Character]\n%s\n\n---\n\n", characterSettings)
}

// FormatEntityProfileSection formats an EntityProfile narrative for context injection.
// The narrative describes the agent's impression of a specific entity (user/agent/session).
// Returns a natural-language section or empty string if narrative is empty.
func FormatEntityProfileSection(narrative string, entityName string) string {
	if narrative == "" {
		return ""
	}
	return fmt.Sprintf("You have formed the following impression about %s:\n\n%s\n\n---\n\n", entityName, narrative)
}

// formatSegmentsSection formats relevant segments as an independent section.
// Segments are RAG-retrieved historical fragments placed with narrative transition,
// since they could not be fused into the pre-generated cached narrative.
// Returns "Some additional details from earlier conversations...\n{items}\n\n" or empty string.
func formatSegmentsSection(relevantSegments []comprehend.Segment) string {
	if len(relevantSegments) == 0 {
		return ""
	}

	var segmentsText []string
	for _, seg := range relevantSegments {
		sourceLabel := "ChatHistory"
		if seg.Source == comprehend.SourceKnowledgeBase {
			sourceLabel = "KnowledgeBase"
		}
		segmentsText = append(segmentsText, fmt.Sprintf("- (%s) %s", sourceLabel, seg.Content))
	}

	return fmt.Sprintf("Some additional details from earlier conversations that may be relevant:\n\n%s\n\n", strings.Join(segmentsText, "\n"))
}

// formatPersonStateInstruction formats person state as natural language instruction.
// Placed in the instruction area (after narrative, before response directive)
// to preserve narrative flow while guiding response strategy.
// Returns "{description}\nAdjust your response tone, detail level, and strategy accordingly.\n\n" or empty string.
func formatPersonStateInstruction(personStateDescription string) string {
	if personStateDescription == "" {
		return ""
	}
	return fmt.Sprintf("%s\nAdjust your response tone, detail level, and strategy accordingly.\n\n", personStateDescription)
}

// formatGuidanceSection formats the Decide phase's execution intent as a
// self-instruction section. Placed after person state and before the response
// directive. The guidance is written in first-person as the character's own
// internal intention — a self-directed thought, not an external command.
// Returns "[Your Intention]\n{guidance}\n\n" or empty string.
func formatGuidanceSection(guidance string) string {
	if guidance == "" {
		return ""
	}
	return fmt.Sprintf("[Your Intention]\n%s\n\n", guidance)
}

// formatTaskResultSection formats agent delivery section for the prompt.
// Provides execution status and results for LLM to formulate response:
//   - success: includes result content and delivery guidance
//   - failure: includes reason and progress notes
func formatTaskResultSection(taskResult *TaskResultForAssembly) string {
	if taskResult == nil {
		return ""
	}

	if taskResult.Status == "success" {
		result := "Task completed."
		if taskResult.Result != "" {
			result = taskResult.Result
		}
		return fmt.Sprintf("[Task Execution Result]\nThe following task was completed successfully:\n\n%s\n\n[Delivery Instructions]\nYou must now let the person you are talking to know about the completed task:\n- If files were delivered via deliver_to, the recipient can find them in their Received area — do NOT include file paths or directory locations\n- If the result is information, present it directly in your response\n- Use your character's tone and style\n\n---\n\n", result)
	}

	notesSection := ""
	if taskResult.Notes != "" {
		notesSection = fmt.Sprintf("\n\nProgress notes:\n%s", taskResult.Notes)
	}

	reason := "Unknown error"
	if taskResult.Reason != "" {
		reason = taskResult.Reason
	}

	return fmt.Sprintf("[Task Execution Interrupted]\nThe task could not be completed.\n\nReason: %s%s\n\n---\n\n", reason, notesSection)
}

// AssembleContext assembles context into one big message for LLM processing.
//
// This method combines character settings, relevant memories, background story
// (cached narrative), relevant segments, and recent messages into a unified
// message format.
//
// The background story is a cached narrative generated in background alongside
// the summary. Segments are RAG-retrieved fragments placed as an independent
// section with narrative transition, since they could not be fused into the
// pre-generated narrative.
//
// Memories are injected between character settings and background story,
// providing cross-session context for the agent.
//
// Parameters:
//   - characterSettings: agent's personality, style, and identity settings
//   - memories: formatted memory context section (may be empty)
//   - backgroundStory: cached narrative from summary record
//   - recentMessages: recent completed messages (including trigger_message as the latest)
//   - relevantSegments: RAG-retrieved historical segments (independent section)
//   - summaryVersion: version number of the summary (covers messages 1 to summaryVersion)
//   - personStateDescription: natural language description of inferred person state,
//     placed in instruction area to guide response strategy
//   - taskResult: agent execution result for world-interaction tasks,
//     provides execution status and results for LLM to formulate response
//   - guidance: execution intent from the Decide phase, placed as self-instruction
//     after person state and before the response directive
func AssembleContext(
	characterSettings string,
	entityProfiles string,
	backgroundStory string,
	recentMessages []model.Message,
	relevantSegments []comprehend.Segment,
	summaryVersion int,
	personStateDescription string,
	taskResult *TaskResultForAssembly,
	userName string,
	guidance string,
) []llm.Message {
	characterSection := formatCharacterSection(characterSettings)
	personStateInstruction := formatPersonStateInstruction(personStateDescription)
	taskResultSection := formatTaskResultSection(taskResult)
	guidanceSection := formatGuidanceSection(guidance)

	userRole := userName

	userPersonID, err := dops.GetCurrentUserPersonID()
	if err != nil {
		applogger.Error("AssembleContext: failed to get current user person ID", "error", err)
	}

	var dialogLines []string
	for _, msg := range recentMessages {
		role := userRole
		if userPersonID != 0 && msg.PersonID != userPersonID {
			role = "You"
		}
		dialogLines = append(dialogLines, fmt.Sprintf("%s [%s]: %s", role, msg.CreatedAt.Format("2006-01-02 15:04:05"), msg.Content))
	}
	dialogSection := strings.Join(dialogLines, "\n")

	var oneBigMessage string
	segmentsSection := formatSegmentsSection(relevantSegments)
	if backgroundStory != "" && summaryVersion != -1 {
		oneBigMessage = fmt.Sprintf(oneBigMessageTemplate,
			characterSection,
			entityProfiles,
			backgroundStory,
			dialogSection,
			segmentsSection,
			taskResultSection,
			personStateInstruction,
			guidanceSection,
		)
	} else {
		oneBigMessage = fmt.Sprintf(oneBigMessageNoStoryTemplate,
			characterSection,
			entityProfiles,
			dialogSection,
			segmentsSection,
			taskResultSection,
			personStateInstruction,
			guidanceSection,
		)
	}

	messages := []llm.Message{
		{Role: "user", Content: oneBigMessage},
	}

	applogger.Info("Assembled context",
		"message_count", len(messages),
		"has_person_state", personStateDescription != "",
		"has_task_result", taskResult != nil,
		"segments", len(relevantSegments),
	)

	return messages
}
