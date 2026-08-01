package model

import "time"

// AgentState persists the dynamic runtime state of an agent.
//
// Associated with Person (not AgentConfig) because energy is a property of
// the agent's identity, not its static configuration. Designed for
// extensibility — future runtime state fields (mood, fatigue, etc.) can be
// added here.
//
// Data constraints are enforced at the application layer, not via
// database-level foreign keys.
type AgentState struct {
	ID                int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID          int64     `gorm:"not null;uniqueIndex;column:person_id" json:"person_id"`                   // FK to persons table (AI agent's identity)
	Energy            int       `gorm:"not null;default:100;column:energy" json:"energy"`                         // Current energy, range [0, 200]
	LastRecoveredDate string    `gorm:"not null;type:text;column:last_recovered_date" json:"last_recovered_date"` // Date "YYYY-MM-DD" in the global fixed timezone
	SleepSince        string    `gorm:"not null;type:text;default:'';column:sleep_since" json:"sleep_since"`
	CreatedAt         time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt         time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for AgentState.
func (AgentState) TableName() string { return "agent_states" }
