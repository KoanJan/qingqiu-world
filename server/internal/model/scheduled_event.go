package model

import "time"

// ScheduledEventStatus constants.
type ScheduledEventStatus int

const (
	// ScheduledEventStatusPending indicates the event is waiting to be triggered.
	ScheduledEventStatusPending ScheduledEventStatus = 0
	// ScheduledEventStatusTriggered indicates the event has been fired.
	ScheduledEventStatusTriggered ScheduledEventStatus = 1
	// ScheduledEventStatusCancelled indicates the event was cancelled.
	ScheduledEventStatusCancelled ScheduledEventStatus = -1
)

// ScheduledEventAction constants.
//
// When a scheduled event fires, the action determines how the runtime processes it:
//   - ActionFullPipeline: the event goes through the normal chat pipeline
//     (context engineering, LLM inference, etc.)
//   - ActionSendMessage: the event directly sends ActionContent as a message
//     to the user, skipping the LLM entirely (fast path)
type ScheduledEventAction int

const (
	// ScheduledEventActionFullPipeline routes the event through the normal chat pipeline.
	ScheduledEventActionFullPipeline ScheduledEventAction = iota
	// ScheduledEventActionSendMessage sends the content directly as a message.
	ScheduledEventActionSendMessage
)

// ScheduledEvent represents a future event that will wake the agent at a specified time.
//
// This is the person's self-wake mechanism: "WakeMeWhen 3pm" means the person
// creates a ScheduledEvent with trigger_at=3pm. When the time arrives, the
// event loop injects an AgentEvent, and the agent responds as if prompted.
//
// Key design:
//   - The agent creates this via the WakeMeWhen tool during a ReAct loop
//   - The alarm context (Message) is the agent's note to its future self,
//     injected as supplementary context alongside the original trigger message.
//   - Action determines whether the event takes the fast path (direct message)
//     or the full pipeline path. The agent decides this at alarm-creation time.
//   - ActionContent is the pre-computed message content for the fast path.
type ScheduledEvent struct {
	ID            int64                `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID      int64                `gorm:"not null;index:idx_se_person_status" json:"person_id"`
	SessionID     int64                `gorm:"not null" json:"session_id"`
	TriggerAt     time.Time            `gorm:"not null;index:idx_trigger" json:"trigger_at"`
	Message       string               `gorm:"type:text;not null" json:"message"`                   // Agent's note to its future self
	Action        ScheduledEventAction `gorm:"not null;default:0" json:"action"`                    // ScheduledEventAction* constant
	ActionContent string               `gorm:"type:text;not null;default:''" json:"action_content"` // Pre-computed content for fast path
	Status        ScheduledEventStatus `gorm:"not null;default:0;index:idx_se_agent_status" json:"status"`
	CreatedAt     time.Time            `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time            `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for ScheduledEvent.
func (ScheduledEvent) TableName() string { return "scheduled_events" }
