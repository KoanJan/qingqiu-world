package model

import "time"

// AgentConfig represents the configuration for an AI assistant.
//
// An AgentConfig defines the behavior and capabilities of an AI assistant,
// including its character settings (personality, style, identity)
// and the LLM/embedding configurations to use.
// Identity (name, bio) is stored in the persons table via PersonID.
type AgentConfig struct {
	ID                int64     `gorm:"primaryKey;autoIncrement;type:INTEGER PRIMARY KEY AUTOINCREMENT" json:"id"`
	PersonID          int64     `gorm:"not null;uniqueIndex;column:person_id;default:0" json:"person_id"`
	CharacterSettings string    `gorm:"type:text;not null;default:'';column:character_settings" json:"character_settings"` // Agent's personality, style, identity
	LLMConfigID       int64     `gorm:"not null;index;column:llm_config_id" json:"llm_config_id"`
	CreatedAt         time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt         time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for AgentConfig.
func (AgentConfig) TableName() string { return "agent_configs" }
