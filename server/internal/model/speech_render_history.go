package model

import "time"

// SpeechRenderStatus represents the durable delivery state of one message audio.
type SpeechRenderStatus int

const (
	// SpeechRenderStatusPending is queued and waiting for the render worker.
	SpeechRenderStatusPending SpeechRenderStatus = 0
	// SpeechRenderStatusRendering is currently owned by the single render worker.
	SpeechRenderStatusRendering SpeechRenderStatus = 1
	// SpeechRenderStatusSuccess holds a playable audio artifact. It is terminal:
	// a successful render is never repeated.
	SpeechRenderStatusSuccess SpeechRenderStatus = 2
	// SpeechRenderStatusFailed records a retryable failure. A later play request
	// resets it to pending.
	SpeechRenderStatusFailed SpeechRenderStatus = 3
)

// SpeechRenderHistory is the single durable record that owns the on-demand audio
// of one Agent message. It doubles as the render worker's queue entry, so every
// column is either worker state or synthesis provenance.
//
// Provenance consists of the immutable AgentVoice version and the adapter-owned
// snapshot of the actual provider request. Mutable worker state remains on this
// row because one Message owns exactly one render job and one successful audio.
type SpeechRenderHistory struct {
	ID int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// MessageID is the Agent message this audio belongs to. It is unique so one
	// message can never produce two competing audio artifacts.
	MessageID int64 `gorm:"not null;uniqueIndex;column:message_id" json:"message_id"`
	// AgentVoiceID identifies the immutable voice configuration selected at the
	// start of the successful render attempt. It stays 0 until an attempt succeeds.
	AgentVoiceID int64 `gorm:"not null;default:0;index;column:agent_voice_id" json:"agent_voice_id"`
	// AdapterRequestSnapshot is a secret-free JSON object containing the key
	// provider request inputs actually used for a successful synthesis. Its
	// schema is adapter-owned; for example Fish Audio records the exact text
	// including its expression cue and all non-secret synthesis parameters.
	AdapterRequestSnapshot string `gorm:"type:text;not null;default:'';column:adapter_request_snapshot" json:"adapter_request_snapshot"`
	// Status is the current SpeechRenderStatus of this delivery.
	Status SpeechRenderStatus `gorm:"not null;default:0" json:"status"`
	// RelativePath is the stored audio path relative to DATA_ROOT/speech. Its file
	// extension carries the audio format, so no separate format column is needed.
	RelativePath string `gorm:"type:text;not null;default:'';column:relative_path" json:"relative_path"`
	// MIMEType is the Content-Type served when this audio is played back. It comes
	// from the provider response and can differ from the format-derived default.
	MIMEType string `gorm:"type:varchar(128);not null;default:'';column:mime_type" json:"mime_type"`
	// AttemptCount counts how many times the worker has started this render.
	AttemptCount int `gorm:"not null;default:0;column:attempt_count" json:"attempt_count"`
	// ErrorMessage holds the last failure reason; it is cleared on retry.
	ErrorMessage string    `gorm:"type:text;not null;default:'';column:error_message" json:"error_message"`
	CreatedAt    time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the table name for speech delivery records.
func (SpeechRenderHistory) TableName() string { return "speech_render_histories" }
