package model

import "time"

// AgentObservation represents an agent's record of having observed an event.
//
// This table serves two layers:
//   - Observation layer: the immutable fact that a person experienced an event
//   - Memory layer: the agent's cognitive assessment of the event (scores),
//     which can decay and be forgotten
//
// Event content is NOT cached here — it is retrieved on demand via
// event_id → events → (event_type, ref_id) → originating table.
// Data constraints are enforced at the application layer, not via
// database-level foreign keys.
type AgentObservation struct {
	ID             int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID       int64     `gorm:"not null;uniqueIndex:idx_observations_person_event;index:idx_observations_person_created,priority:1;column:person_id" json:"person_id"`
	EventID        int64     `gorm:"not null;uniqueIndex:idx_observations_person_event;column:event_id" json:"event_id"`
	Importance     float64   `gorm:"not null;default:0.5" json:"importance"`
	LastAccessedAt time.Time `gorm:"not null;default:CURRENT_TIMESTAMP;column:last_accessed_at" json:"last_accessed_at"`
	LastScoredAt   time.Time `gorm:"not null;default:CURRENT_TIMESTAMP;column:last_scored_at" json:"last_scored_at"`
	CreatedAt      time.Time `gorm:"not null;autoCreateTime;index:idx_observations_person_created,priority:2;index:idx_observations_created" json:"created_at"`
	UpdatedAt      time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for AgentObservation.
func (AgentObservation) TableName() string { return "agent_observations" }
