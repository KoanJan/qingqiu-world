package eventqueue

import (
	"fmt"
	"strings"

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
	// EventTypeBiography represents the system delivering the agent its own origin
	// record — informing the agent that it came into existence at a specific time.
	EventTypeBiography
	// EventTypeNewJinshuReceived represents another person delivering a jinshu
	// to this agent.
	EventTypeNewJinshuReceived
	// EventTypeJinshuReadCompleted represents the agent finishing reading a
	// received jinshu through the dedicated read loop.
	EventTypeJinshuReadCompleted
	// EventTypeJinshuListed represents the result of a paginated keyword search
	// over the agent's received jinshu, produced by the ListReceivedJinshu action.
	EventTypeJinshuListed
	// EventTypeJinshuSent represents the result of the agent sending files from
	// its private space as a jinshu, produced by the SendJinshu action.
	EventTypeJinshuSent
	// EventTypeJinshuSentListed represents the result of a paginated keyword
	// search over the agent's sent jinshu, produced by the ListSentJinshu action.
	EventTypeJinshuSentListed
)

// AgentEvent represents an event that should be processed by an agent.
type AgentEvent struct {
	Payload   any // Type depends on the event type
	Type      AgentEventType
	SessionID int64
	EventID   int64 // Memory system event record ID (0 if no memory event)

	// TriggerAction carries the originating Action's cognitive context when
	// this event was produced by the agent's own Decide output. It is nil for
	// externally-triggered events (e.g., system-initiated work).
	TriggerAction *TriggerAction
}

// TriggerAction carries the originating Action's cognitive context for
// provenance. It holds only Background and Reason, not the full action.Action
// plan pointers, because downstream consumers only need the "why" and
// "what triggered it" — and importing the full action package here would
// create an import cycle.
type TriggerAction struct {
	Background string // What situation triggered the originating decision
	Reason     string // Why the originating decision was made
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
	case EventTypeBiography:
		p, ok := e.Payload.(*BiographyPayload)
		if !ok || p == nil {
			return "[Biography]"
		}
		return p.Content
	case EventTypeNewJinshuReceived:
		p, ok := e.Payload.(*JinshuReceivedPayload)
		if !ok || p == nil {
			return "[Jinshu received]"
		}
		files := ""
		if len(p.Files) > 0 {
			files = fmt.Sprintf(" (files: %s)", strings.Join(p.Files, ", "))
		}
		return fmt.Sprintf("[Jinshu received] jinshu_id=%d from \"%s\" (topic: \"%s\"): \"%s\"%s",
			p.JinshuID, p.FromName, p.Topic, p.Description, files)
	case EventTypeJinshuReadCompleted:
		p, ok := e.Payload.(*JinshuReadCompletedPayload)
		if !ok || p == nil {
			return "[Jinshu read completed]"
		}
		if p.Status == "success" {
			return fmt.Sprintf("[Jinshu read completed] You read jinshu #%d from \"%s\" (topic: \"%s\"). Summary: %s",
				p.JinshuID, p.FromName, p.Topic, p.Summary)
		}
		return fmt.Sprintf("[Jinshu read failed] jinshu #%d from \"%s\": %s",
			p.JinshuID, p.FromName, p.Error)
	case EventTypeJinshuListed:
		p, ok := e.Payload.(*JinshuListedPayload)
		if !ok || p == nil {
			return "[Jinshu list]"
		}
		var sb strings.Builder
		if p.Query != "" {
			fmt.Fprintf(&sb, "[Jinshu list] page %d (query: %q):", p.Page, p.Query)
		} else {
			fmt.Fprintf(&sb, "[Jinshu list] page %d:", p.Page)
		}
		if len(p.Results) == 0 {
			sb.WriteString(" no results")
			return sb.String()
		}
		for _, r := range p.Results {
			read := "unread"
			if r.IsRead {
				read = "read"
			}
			fmt.Fprintf(&sb, "\n- jinshu_id=%d from %q topic %q (%s, %s)",
				r.JinshuID, r.FromName, r.Topic, read, r.CreatedAt)
		}
		return sb.String()
	case EventTypeJinshuSent:
		p, ok := e.Payload.(*JinshuSentPayload)
		if !ok || p == nil {
			return "[Jinshu sent]"
		}
		if p.Status == "success" {
			return fmt.Sprintf("[Jinshu sent] You sent jinshu #%d to %q (topic: %q).",
				p.JinshuID, p.ToName, p.Topic)
		}
		return fmt.Sprintf("[Jinshu send failed] to %q (topic: %q): %s",
			p.ToName, p.Topic, p.Error)
	case EventTypeJinshuSentListed:
		p, ok := e.Payload.(*JinshuSentListedPayload)
		if !ok || p == nil {
			return "[Jinshu sent list]"
		}
		var sb strings.Builder
		if p.Query != "" {
			fmt.Fprintf(&sb, "[Jinshu sent list] page %d (query: %q):", p.Page, p.Query)
		} else {
			fmt.Fprintf(&sb, "[Jinshu sent list] page %d:", p.Page)
		}
		if len(p.Results) == 0 {
			sb.WriteString(" no results")
			return sb.String()
		}
		for _, r := range p.Results {
			fmt.Fprintf(&sb, "\n- jinshu_id=%d to %q topic %q (%s)",
				r.JinshuID, r.ToName, r.Topic, r.CreatedAt)
		}
		return sb.String()
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
//
// The originating Action's provenance is carried by AgentEvent.TriggerAction,
// not this payload.
type WorkCompletedPayload struct {
	WorkID     int64  // ID of the completed work
	Guidance   string // The original guidance (execution intent) of the work
	Status     string // "success" or "failure"
	TaskOutput string // Task execution output (for TaskWork success)
	TaskError  string // Task execution error (for TaskWork failure)
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

// BiographyPayload is the payload type for EventTypeBiography events.
// It carries the agent's own origin record — a factual statement that the
// agent came into existence at a specific time. The agent processes this as
// a self-orienting event, not as a message from another Person.
type BiographyPayload struct {
	BiographyID int64  // ID of the AgentBiography record
	Content     string // Natural-language origin statement shown to the agent
}

// JinshuReceivedPayload is the payload type for EventTypeNewJinshuReceived events.
// It carries enough context for the recipient to decide how to react without
// having to load the jinshu record itself.
type JinshuReceivedPayload struct {
	JinshuID    int64    // ID of the Jinshu record
	FromName    string   // Display name of the sender
	Topic       string   // Short subject of the jinshu
	Description string   // Optional sender note
	Files       []string // Delivered file/directory relative paths (so the agent knows what it received)
}

// JinshuReadCompletedPayload is the payload type for EventTypeJinshuReadCompleted.
// When the dedicated jinshu-read loop finishes, the agent receives this event
// so it can decide how to react to the content it just read (usually chat).
type JinshuReadCompletedPayload struct {
	JinshuID int64  // ID of the Jinshu record
	FromName string // Display name of the sender
	Topic    string // Short subject of the jinshu
	Summary  string // The agent's own understanding/summary of the jinshu content
	Status   string // "success" or "failure"
	Error    string // Reading error (for failure)
}

// JinshuListItem is a single received jinshu in a JinshuListedPayload result.
type JinshuListItem struct {
	JinshuID  int64  // ID of the Jinshu record
	FromName  string // Display name of the sender
	Topic     string // Short subject of the jinshu
	IsRead    bool   // Receiver-only read flag
	CreatedAt string // Creation time formatted as "2006-01-02 15:04"
}

// JinshuListedPayload is the payload type for EventTypeJinshuListed events.
// When the agent's ListReceivedJinshu action runs, the paginated keyword search result
// flows back through this event so the agent can pick a jinshu_id to inspect.
type JinshuListedPayload struct {
	Query   string           // The keyword used for filtering (empty means all)
	Page    int              // The 1-based page number returned
	Results []JinshuListItem // The matching received jinshu, newest first
}

// JinshuSentPayload is the payload type for EventTypeJinshuSent events.
// It reports the outcome of the agent's SendJinshu action back to itself so it
// knows whether the delivery succeeded (or can react to a failure).
type JinshuSentPayload struct {
	JinshuID int64  // ID of the created Jinshu record (0 on failure)
	ToName   string // Display name of the recipient
	Topic    string // Short subject of the jinshu
	Status   string // "success" or "failure"
	Error    string // Send error (for failure)
}

// JinshuSentListItem is a single sent jinshu in a JinshuSentListedPayload result.
type JinshuSentListItem struct {
	JinshuID  int64  // ID of the Jinshu record
	ToName    string // Display name of the recipient
	Topic     string // Short subject of the jinshu
	CreatedAt string // Creation time formatted as "2006-01-02 15:04"
}

// JinshuSentListedPayload is the payload type for EventTypeJinshuSentListed.
// When the agent's ListSentJinshu action runs, the paginated keyword search
// result over its outbound deliveries flows back through this event so the
// agent can recall what it has already sent.
type JinshuSentListedPayload struct {
	Query   string               // The keyword used for filtering (empty means all)
	Page    int                  // The 1-based page number returned
	Results []JinshuSentListItem // The matching sent jinshu, newest first
}
