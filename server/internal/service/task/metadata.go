package task

import "fmt"

// SourceType identifies what triggered this work.
type SourceType int

const (
	// SourceTypeNone indicates no specific trigger source.
	SourceTypeNone SourceType = 0
	// SourceTypeSession indicates the work was triggered by a chat session message.
	SourceTypeSession SourceType = 1
	// SourceTypeScheduled indicates the work was triggered by a scheduled self-reminder.
	SourceTypeScheduled SourceType = 2
	// SourceTypeWorkCompleted indicates the work was triggered by completion of previous work.
	SourceTypeWorkCompleted SourceType = 3
)

// SessionMeta holds session-related traceability info for Metadata.
type SessionMeta struct {
	SessionID  int64
	Trigger    string
	SenderName string // Who sent the triggering message
}

// Metadata carries system-generated traceability info from work creation.
// It is injected into the task loop's system prompt as a [Metadata] section
// so the agent knows where it came from and can use tools like
// search_chat_histories with the correct session context.
type Metadata struct {
	SourceType  SourceType
	SessionMeta *SessionMeta // nil for non-session sources
}

// String returns a human-readable summary of the metadata for system prompt injection.
func (m Metadata) String() string {
	switch m.SourceType {
	case SourceTypeSession:
		if m.SessionMeta == nil {
			return "Trigger: chat message (unknown session)"
		}
		return fmt.Sprintf("Trigger: %s", m.SessionMeta.Trigger)
	case SourceTypeScheduled:
		return "Trigger: self-reminder alarm"
	case SourceTypeWorkCompleted:
		if m.SessionMeta == nil {
			return "Trigger: previous work completed"
		}
		return fmt.Sprintf("Trigger: %s", m.SessionMeta.Trigger)
	default:
		return ""
	}
}
