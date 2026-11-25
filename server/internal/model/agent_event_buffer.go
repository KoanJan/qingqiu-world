package model

import "time"

// AgentEventBuffer persists one intended Agent delivery until the queue returns it.
type AgentEventBuffer struct {
	ID          int64     `gorm:"primaryKey;autoIncrement;index:idx_agent_event_buffer_person_id,priority:2" json:"id"`
	PersonID    int64     `gorm:"not null;uniqueIndex:idx_agent_event_buffer_person_event;index:idx_agent_event_buffer_person_id,priority:1;column:person_id" json:"person_id"`
	EventType   int       `gorm:"not null;column:event_type" json:"event_type"`
	SessionID   int64     `gorm:"not null;column:session_id" json:"session_id"`
	EventID     int64     `gorm:"not null;uniqueIndex:idx_agent_event_buffer_person_event;column:event_id" json:"event_id"`
	PayloadJSON string    `gorm:"not null;type:text;column:payload_json" json:"payload_json"`
	CreatedAt   time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt   time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the persistent table name for AgentEventBuffer.
func (AgentEventBuffer) TableName() string { return "agent_event_buffers" }
