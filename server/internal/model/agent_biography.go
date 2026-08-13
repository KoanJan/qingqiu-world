package model

import "time"

// AgentBiography stores an agent's origin record — the factual statement that
// the agent came into existence at a specific time in Qingqiu World.
//
// Content holds the natural-language description delivered to the agent; it is
// the payload behind a biography event (events.event_type = EventTypeBiography).
// Each agent has exactly one origin record, hence the unique person_id index.
type AgentBiography struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID  int64     `gorm:"not null;uniqueIndex;column:person_id" json:"person_id"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName returns the database table name for AgentBiography.
func (AgentBiography) TableName() string { return "agent_biographies" }
