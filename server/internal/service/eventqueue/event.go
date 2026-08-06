package eventqueue

import (
	"fmt"
	"qingqiu-world-server/internal/model"
)

// ---------------------------------------------------------------------------
// Event types
// ---------------------------------------------------------------------------

// AgentEventType represents the type of an agent event.
type AgentEventType int

const (
	// EventTypeNewPrivateChatMessage represents a user or agent message in a private chat.
	EventTypeNewPrivateChatMessage AgentEventType = iota
	// EventTypeGroupChatJoined represents the agent being added to a group chat.
	EventTypeGroupChatJoined
	// EventTypeGroupChatLeft represents the agent being removed from a group chat.
	EventTypeGroupChatLeft
	// EventTypeSystemNotification represents a system-level notification.
	EventTypeSystemNotification
	// EventTypeScheduled represents a scheduled event (self-wake alarm) that has fired.
	EventTypeScheduled
	// EventTypeWorkCompleted represents a Work (task/chat) completing execution.
	EventTypeWorkCompleted
	// EventTypeAlarmCreated represents a new scheduled alarm being created (by tool or recovery).
	EventTypeAlarmCreated
	// EventTypeHeartbeat represents a heartbeat tick that grants the agent an
	// autonomous cognitive opportunity. This is not a fake external message —
	// it is the world itself signaling "time has passed, you are idle, you may
	// now form an intention if you wish." The agent may decide to act (begin
	// a conversation, set an alarm) or decide to do nothing.
	EventTypeHeartbeat
)

// AgentEvent represents an event that should be processed by an agent.
type AgentEvent struct {
	Payload   any // Type depends on the event type
	Type      AgentEventType
	SessionID int64
	EventID   int64 // Memory system event record ID (0 if no memory event)
}

// FormatDescription formats the event as natural language for LLM consumption.
// Different event sources carry different semantics — the LLM needs to
// distinguish between someone speaking to you and a self-triggered alarm.
//
// The payload carries all necessary context (e.g., SpeakerName for messages),
// so this method needs no external parameters.
func (e AgentEvent) FormatDescription() string {
	switch e.Type {
	case EventTypeNewPrivateChatMessage:
		p, ok := e.Payload.(*NewMessagePayload)
		if !ok || p == nil {
			return ""
		}
		return fmt.Sprintf("[Private chat] \"%s\" talks to you: \"%s\"", p.SpeakerName, p.MessageContent)
	case EventTypeScheduled:
		p, ok := e.Payload.(*ScheduledEventPayload)
		if !ok || p == nil {
			return "[Scheduled alarm]"
		}
		return fmt.Sprintf("[Scheduled alarm] %s", p.Message)
	case EventTypeWorkCompleted:
		p, ok := e.Payload.(*WorkCompletedPayload)
		if !ok || p == nil {
			return "[Work completed]"
		}
		return fmt.Sprintf("[Work completed] %s (status: %s)", p.Guidance, p.Status)
	case EventTypeHeartbeat:
		// Heartbeat is the world signaling "time has passed, you are idle."
		// It carries no payload — the agent's situation is the context.
		return "[Heartbeat] Time has passed. You are idle and may form an intention."
	default:
		return ""
	}
}

// NewMessagePayload is the payload type for EventTypeNewMessage events.
//
// SpeakerName is the display name of whoever sent the message.
// In 1v1 sessions this is the person's name; in future group chat,
// it may be a person's name or another agent's name.
type NewMessagePayload struct {
	MessageID      int64
	MessageContent string
	SpeakerName    string // Display name of the message sender
}

// ScheduledEventPayload is the payload type for EventTypeScheduled events.
// When a scheduled alarm fires, the agent receives this payload so it can
// recall why it set the alarm and what to do.
//
// Scheduled events are transient triggers — they carry business context but
// do NOT persist records in the messages table. Instead:
//   - Message carries the agent's note to its future self, injected as
//     supplementary context in the pipeline
//   - Action determines whether the runtime takes the fast path (direct
//     message) or the full pipeline path
//   - ActionContent carries the pre-computed message for the fast path
type ScheduledEventPayload struct {
	ScheduledEventID int64                      // ID of the ScheduledEvent record
	Message          string                     // Agent's note to its future self when the alarm fires
	Action           model.ScheduledEventAction // model.ScheduledEventAction* constant
	ActionContent    string                     // Pre-computed message content for fast path (ActionSendMessage)
}

// WorkCompletedPayload is the payload type for EventTypeWorkCompleted events.
// When a Work finishes execution (success or failure), the agent receives this
// event so it can decide whether to inform the user or take other action.
//
// This represents the agent's self-perception: "I just finished doing X."
// The agent processes it through the same Comprehend→Decide pipeline as
// external events, ensuring consistent cognitive handling.
type WorkCompletedPayload struct {
	WorkID     int64  // ID of the completed work
	WorkType   int    // model.WorkTypeChat or model.WorkTypeTask
	Guidance   string // The original guidance (execution intent) of the work
	Status     string // "success" or "failure"
	TaskOutput string // Task execution output (for TaskWork success)
	TaskError  string // Task execution error (for TaskWork failure)
	Trigger    string // Semantic description of the completed work's cause
}

// AlarmCreatedPayload is the payload type for EventTypeAlarmCreated events.
// When a tool (or recovery logic) creates a new scheduled alarm, this event
// notifies the runtime so it can register a goroutine to wait for the trigger time.
//
// The runtime is the sole manager of alarm goroutines — tools only create DB
// records and send this event. This avoids circular dependencies and keeps
// goroutine lifecycle management centralized.
type AlarmCreatedPayload struct {
	ScheduledEventID int64 // ID of the newly created ScheduledEvent record
}
