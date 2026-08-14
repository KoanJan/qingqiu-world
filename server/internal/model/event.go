package model

import "time"

type EventType int

// Event type constants. Each type represents a different kind of external event
// that agents can observe. New types are added as the system evolves.
const (
	EventTypeMessage   EventType = iota + 1 // A message in a session (user or agent)
	EventTypeBiography                      // An agent's origin record (its beginning in the world)
	EventTypeJinshu                         // A jinshu delivery from one person to another
)

// Event represents an external event in the unified event table.
//
// The event table is a single entry point for all external events. It stores
// only the event type and a reference to the original record, not the payload
// itself. Actual content is retrieved from the originating table via
// (event_type, ref_id) lookup.
//
// Events are pure occurrences and do not carry observer information. The
// observation layer (agent_observations) handles which agents have seen
// each event.
type Event struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	EventType EventType `gorm:"not null;index:idx_events_type_created;column:event_type" json:"event_type"`
	RefID     int64     `gorm:"not null;column:ref_id" json:"ref_id"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime;index:idx_events_type_created" json:"created_at"`
}

// TableName returns the database table name for Event.
func (Event) TableName() string { return "events" }
