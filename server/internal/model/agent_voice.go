package model

import "time"

// AgentVoice is one immutable version of an Agent's voice configuration. A
// newer configuration is inserted as another row; the greatest ID for one
// PersonID is the current version. RendererVoiceRef is derived runtime state
// and is the only field that may be populated after insertion.
type AgentVoice struct {
	ID                      int64     `gorm:"primaryKey;autoIncrement;index:idx_agent_voice_latest,priority:2" json:"id"`
	PersonID                int64     `gorm:"not null;index:idx_agent_voice_latest,priority:1;column:person_id" json:"person_id"`
	TTSRendererID           int64     `gorm:"not null;default:0;column:tts_renderer_id" json:"tts_renderer_id"`
	RendererVoiceRef        string    `gorm:"type:text;not null;default:'';column:renderer_voice_ref" json:"renderer_voice_ref"`
	SampleAudioSHA256       string    `gorm:"type:varchar(64);not null;default:'';column:sample_audio_sha256" json:"-"`
	SampleTranscript        string    `gorm:"type:text;not null;default:'';column:sample_transcript" json:"sample_transcript"`
	SampleAudioExtension    string    `gorm:"type:varchar(16);not null;default:'';column:sample_audio_extension" json:"sample_audio_extension"`
	SampleAudioRelativePath string    `gorm:"type:varchar(500);not null;default:'';column:sample_audio_relative_path" json:"sample_audio_relative_path"`
	SampleLocale            string    `gorm:"type:varchar(64);not null;default:'';column:sample_locale" json:"sample_locale"`
	CreatedAt               time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt               time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the table name for agent voices.
func (AgentVoice) TableName() string { return "agent_voices" }
