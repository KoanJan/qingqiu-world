package comprehend

import (
	"context"
	"fmt"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/comprehend/chat"
	"qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/eventqueue"
)

// Comprehend performs the comprehension phase: understanding the incoming
// event before any judgment is made.
//
// It routes by event type to the appropriate event-type-specific sub-package.
// For non-message events there is no "other party" to understand, so the event
// description is used as-is and the full pipeline is skipped.
//
// It returns a non-nil Comprehension for every supported event type. For an
// unsupported event type it returns (nil, error), indicating a programming
// defect: a new event type was added without a comprehension branch.
func Comprehend(
	ctx context.Context,
	event *eventqueue.AgentEvent,
	ac *model.AgentConfig,
	llmConfig *model.LLMConfig,
	activeWorksSummary string,
) (*types.Comprehension, error) {
	c := &types.Comprehension{}
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
	case eventqueue.EventTypeNewJinshuReceived:
		// A jinshu is a delivery from another person, but it is not a chat
		// message inside a session. Its comprehension is the event description
		// itself (sender, topic, description) — no LLM pass is needed here.
		// The agent can decide to inspect it via the dedicated read loop.
		c.Type = types.ComprehensionTypeNone
		c.EventDescription = event.FormatDescription()
	case eventqueue.EventTypeJinshuReadCompleted:
		// The agent's own summary after reading a jinshu. There is no other
		// party to interpret, so the event description (the summary) is used
		// as-is; no LLM pass is needed.
		c.Type = types.ComprehensionTypeNone
		c.EventDescription = event.FormatDescription()
	case eventqueue.EventTypeJinshuListed:
		// The paginated jinshu search result produced by the agent's own
		// ListReceivedJinshu action. The event description carries the list directly,
		// so no LLM pass is needed.
		c.Type = types.ComprehensionTypeNone
		c.EventDescription = event.FormatDescription()
	case eventqueue.EventTypeJinshuSent:
		// The result of the agent's own SendJinshu action. The event description
		// already states success/failure, so no LLM pass is needed.
		c.Type = types.ComprehensionTypeNone
		c.EventDescription = event.FormatDescription()
	case eventqueue.EventTypeJinshuSentListed:
		// The paginated sent-jinshu search result produced by the agent's own
		// ListSentJinshu action. The event description carries the list directly,
		// so no LLM pass is needed.
		c.Type = types.ComprehensionTypeNone
		c.EventDescription = event.FormatDescription()
	case eventqueue.EventTypePSCompleted:
		// A private-space digest is the agent's own activity summary, not a
		// message from another party. Its comprehension is the event
		// description (the digest) as-is — no LLM pass is needed.
		c.Type = types.ComprehensionTypeNone
		c.EventDescription = event.FormatDescription()
	case eventqueue.EventTypeScheduled,
		eventqueue.EventTypeAlarmCreated,
		eventqueue.EventTypeGroupChatJoined,
		eventqueue.EventTypeGroupChatLeft,
		eventqueue.EventTypeSystemNotification:
		// These events carry no "other party" to understand. Their event
		// description is used as-is and no LLM pass is performed.
		c.Type = types.ComprehensionTypeNone
		c.EventDescription = event.FormatDescription()
	default:
		return nil, fmt.Errorf("comprehend: unsupported event type %d (event_id=%d)", event.Type, event.EventID)
	}
	return c, nil
}

// SignalNarrative triggers asynchronous per-agent narrative generation for a
// session. It is re-exported from the chat sub-package because the chat
// execution phase uses it after committing a reply.
var SignalNarrative = chat.SignalNarrative
