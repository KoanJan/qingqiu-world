package model

import "time"

type EventType int

// Event type constants. Each type represents a different kind of external event
// that agents can observe. New types are added as the system evolves.
const (
	EventTypeMessage                EventType = iota + 1 // A message in a session (user or agent)
	EventTypeBiography                                   // An agent's origin record (its beginning in the world)
	EventTypeJinshu                                      // A jinshu delivery from one person to another
	EventTypeWorkCompleted                               // A work's completion record (episodic gist of what the agent did)
	EventTypePSDigest                                    // A private-space session digest (decisions and ideas from one loop run)
	EventTypeScheduled                                   // A scheduled alarm firing
	EventTypeJinshuListed                                // A received-jinshu list result
	EventTypeJinshuSent                                  // A sent-jinshu outcome
	EventTypeJinshuSentListed                            // A sent-jinshu list result
	EventTypeOwnedSpaceInspected                         // A bounded owned-space inspection result
	EventTypeExecutionSlotAvailable                      // A previously requested sustained slot became available
	EventTypeSystemNotification                          // A world fact announced by the system
)

// Event represents an external event in the unified event table.
//
// The event table is a single entry point for all external events. It stores
// either a reference to an originating record or a self-held fact snapshot.
// RefID and PayloadJSON are mutually exclusive; application code validates
// this invariant before insertion.
//
// Events are pure occurrences and do not carry observer information. The
// observation layer (agent_observations) handles which agents have seen
// each event.
type Event struct {
	// ID identifies one occurrence independently of who observed it.
	ID int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// EventType selects the domain interpretation of this occurrence.
	EventType EventType `gorm:"not null;index:idx_events_type_created;column:event_type" json:"event_type"`
	// RefID points to an originating row; zero selects a self-held snapshot.
	RefID int64 `gorm:"not null;column:ref_id" json:"ref_id"`
	// PayloadJSON holds that snapshot and is empty when RefID is nonzero.
	PayloadJSON string `gorm:"type:text;not null;default:'';column:payload_json" json:"payload_json"`
	// CreatedAt records when this occurrence was persisted.
	CreatedAt time.Time `gorm:"not null;autoCreateTime;index:idx_events_type_created" json:"created_at"`
}

// TableName returns the database table name for Event.
func (Event) TableName() string { return "events" }

// EffectTarget identifies the private ActionEffect key that may have produced
// this occurrence. It expresses a recorded source link, never semantic causality.
func (e Event) EffectTarget() (ActionEffectType, int64) {
	if e.RefID == 0 {
		return ActionEffectSelfHeldEvent, e.ID
	}
	switch e.EventType {
	case EventTypeMessage:
		return ActionEffectMessage, e.RefID
	case EventTypeWorkCompleted:
		return ActionEffectWork, e.RefID
	case EventTypeScheduled:
		return ActionEffectScheduledEvent, e.RefID
	case EventTypeJinshu, EventTypeJinshuSent:
		return ActionEffectJinshu, e.RefID
	}
	return 0, 0
}
