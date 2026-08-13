package comprehend

import (
	"context"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/comprehend/chat"
	"qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/eventqueue"

	applogger "qingqiu-world-server/internal/logger"
)

// Comprehend performs the comprehension phase: understanding the incoming
// event before any judgment is made.
//
// It routes by event type to the appropriate event-type-specific sub-package.
// For non-message events there is no "other party" to understand, so the event
// description is used as-is and the full pipeline is skipped.
func Comprehend(
	ctx context.Context,
	event *eventqueue.AgentEvent,
	ac *model.AgentConfig,
	llmConfig *model.LLMConfig,
	activeWorksSummary string,
) *types.Comprehension {
	c := &types.Comprehension{Type: types.ComprehensionTypeInvalid}
	switch event.Type {
	case eventqueue.EventTypeNewPrivateChatMessage:
		c.Type = types.ComprehensionTypeChat
		c.EventDescription, c.Chat = chat.ComprehendMessage(ctx, event, ac, llmConfig, activeWorksSummary)
	case eventqueue.EventTypeBiography:
		// A biography is a self-orienting fact, not a message from another
		// party. Its comprehension is the event description itself — no LLM
		// pass is needed because there is nothing to interpret.
		c.Type = types.ComprehensionTypeBiography
		c.EventDescription = event.FormatDescription()
	case eventqueue.EventTypeWorkCompleted:
		// Work completion is the agent's own execution result, not a message
		// from another party. Its comprehension is the event description
		// itself (guidance plus status) — no LLM pass is needed.
		c.Type = types.ComprehensionTypeWorkCompleted
		c.EventDescription = event.FormatDescription()
	default:
		applogger.Error(
			"Comprehend error, event type not handle",
			"event_id", event.EventID,
			"event_type", event.Type,
			"person_id", ac.PersonID,
		)
	}
	return c
}

// SignalNarrative triggers asynchronous per-agent narrative generation for a
// session. It is re-exported from the chat sub-package because the chat
// execution phase uses it after committing a reply.
var SignalNarrative = chat.SignalNarrative
