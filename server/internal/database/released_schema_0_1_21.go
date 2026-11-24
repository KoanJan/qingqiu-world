package database

import "time"

// releasedLLMConfig freezes the 0.1.21 table shape while development code
// carries probe fields that are not part of that released version.
type releasedLLMConfig struct {
	ID          int64     `gorm:"primaryKey;autoIncrement;type:INTEGER PRIMARY KEY AUTOINCREMENT"`
	Name        string    `gorm:"type:varchar(100);not null"`
	ModelID     string    `gorm:"type:varchar(100);not null;column:model_id"`
	BaseURL     string    `gorm:"type:varchar(255);not null;column:base_url"`
	APIKey      string    `gorm:"type:varchar(255);not null;column:api_key"`
	Description string    `gorm:"type:text;not null;default:''"`
	CreatedAt   time.Time `gorm:"not null;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"not null;autoUpdateTime"`
}

func (releasedLLMConfig) TableName() string { return "llm_configs" }

// releasedAgentEventBuffer preserves the nonunique 0.1.21 delivery index.
// Recipient deduplication and uniqueness belong to development revision 3.
type releasedAgentEventBuffer struct {
	ID          int64     `gorm:"primaryKey;autoIncrement"`
	PersonID    int64     `gorm:"not null;index:idx_agent_event_buffer_person_event;column:person_id"`
	EventType   int       `gorm:"not null;column:event_type"`
	SessionID   int64     `gorm:"not null;column:session_id"`
	EventID     int64     `gorm:"not null;index:idx_agent_event_buffer_person_event;column:event_id"`
	PayloadJSON string    `gorm:"not null;type:text;column:payload_json"`
	CreatedAt   time.Time `gorm:"not null;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"not null;autoUpdateTime"`
}

func (releasedAgentEventBuffer) TableName() string { return "agent_event_buffers" }
