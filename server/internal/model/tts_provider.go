package model

import "time"

// TTSProvider identifies one externally implemented TTS protocol.
type TTSProvider int

const (
	TTSProviderUnknown   TTSProvider = 0
	TTSProviderCosyVoice TTSProvider = 1 // Reserved for the deferred adapter.
	TTSProviderFishAudio TTSProvider = 2
)

// TTSProviderDefinition stores the user-readable configuration contract for a
// registered provider. Provider behavior remains implemented by its adapter.
type TTSProviderDefinition struct {
	ID                         int64       `gorm:"primaryKey;autoIncrement" json:"id"`
	Provider                   TTSProvider `gorm:"not null;uniqueIndex;column:provider" json:"provider"`
	Name                       string      `gorm:"type:varchar(100);not null" json:"name"`
	ConnectionConfigJSONSchema string      `gorm:"type:text;not null;column:connection_config_json_schema" json:"connection_config_json_schema"`
	TermsURL                   string      `gorm:"type:text;not null;default:'';column:terms_url" json:"terms_url"`
	Description                string      `gorm:"type:text;not null;default:''" json:"description"`
	CreatedAt                  time.Time   `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt                  time.Time   `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the table name for provider definitions.
func (TTSProviderDefinition) TableName() string { return "tts_providers" }
