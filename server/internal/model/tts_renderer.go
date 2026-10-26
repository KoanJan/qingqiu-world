package model

import "time"

// TTSRenderer is a user-configured connection to one registered TTS provider.
type TTSRenderer struct {
	ID                   int64       `gorm:"primaryKey;autoIncrement" json:"id"`
	Name                 string      `gorm:"type:varchar(100);not null" json:"name"`
	Provider             TTSProvider `gorm:"not null;index;column:provider" json:"provider"`
	ConnectionConfigJSON string      `gorm:"type:text;not null;column:connection_config_json" json:"connection_config_json"`
	Description          string      `gorm:"type:text;not null;default:''" json:"description"`
	CreatedAt            time.Time   `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt            time.Time   `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the table name for renderer connections.
func (TTSRenderer) TableName() string { return "tts_renderers" }
