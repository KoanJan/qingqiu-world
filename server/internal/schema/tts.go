package schema

import (
	"encoding/json"
	"time"

	"qingqiu-world-server/internal/model"
)

// TTSProviderDefinitionResponse is the public dynamic configuration contract.
type TTSProviderDefinitionResponse struct {
	Provider                   model.TTSProvider `json:"provider"`
	Name                       string            `json:"name"`
	ConnectionConfigJSONSchema string            `json:"connection_config_json_schema"`
	TermsURL                   string            `json:"terms_url"`
	Description                string            `json:"description"`
}

// NewTTSProviderDefinitionResponse converts a provider definition for API use.
func NewTTSProviderDefinitionResponse(definition *model.TTSProviderDefinition) *TTSProviderDefinitionResponse {
	return &TTSProviderDefinitionResponse{
		Provider:                   definition.Provider,
		Name:                       definition.Name,
		ConnectionConfigJSONSchema: definition.ConnectionConfigJSONSchema,
		TermsURL:                   definition.TermsURL,
		Description:                definition.Description,
	}
}

// TTSRendererCreate contains one user-configured provider connection.
type TTSRendererCreate struct {
	Name             string            `json:"name" binding:"required"`
	Provider         model.TTSProvider `json:"provider" binding:"required"`
	ConnectionConfig json.RawMessage   `json:"connection_config" binding:"required"`
	Description      string            `json:"description"`
}

// TTSRendererUpdate contains editable renderer fields. Provider identity is
// immutable because changing protocols would change the resource's meaning.
type TTSRendererUpdate struct {
	Name             string          `json:"name" binding:"required"`
	ConnectionConfig json.RawMessage `json:"connection_config" binding:"required"`
	Description      string          `json:"description"`
}

// TTSRendererResponse returns renderer metadata without connection credentials.
type TTSRendererResponse struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Provider    model.TTSProvider `json:"provider"`
	Description string            `json:"description"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// TTSRendererDetailResponse exposes non-secret connection fields and reports
// configured write-only fields separately, without returning their values.
type TTSRendererDetailResponse struct {
	TTSRendererResponse
	ConnectionConfig       map[string]interface{} `json:"connection_config"`
	ConfiguredSecretFields []string               `json:"configured_secret_fields"`
}

// AgentVoiceResponse exposes the current immutable Agent voice version without
// leaking the sample's internal storage path or provider-local cache.
type AgentVoiceResponse struct {
	ID                  int64     `json:"id"`
	PersonID            int64     `json:"person_id"`
	TTSRendererID       int64     `json:"tts_renderer_id"`
	SampleAudioFilename string    `json:"sample_audio_filename"`
	SampleTranscript    string    `json:"sample_transcript"`
	SampleLocale        string    `json:"sample_locale"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// NewAgentVoiceResponse converts one immutable voice version for API use.
func NewAgentVoiceResponse(voice *model.AgentVoice) *AgentVoiceResponse {
	return &AgentVoiceResponse{
		ID:                  voice.ID,
		PersonID:            voice.PersonID,
		TTSRendererID:       voice.TTSRendererID,
		SampleAudioFilename: sampleAudioFilename(voice),
		SampleTranscript:    voice.SampleTranscript,
		SampleLocale:        voice.SampleLocale,
		CreatedAt:           voice.CreatedAt,
		UpdatedAt:           voice.UpdatedAt,
	}
}

func sampleAudioFilename(voice *model.AgentVoice) string {
	if voice.SampleAudioExtension == "" {
		return ""
	}
	return "sample" + voice.SampleAudioExtension
}

// NewTTSRendererResponse converts one renderer for API use.
func NewTTSRendererResponse(renderer *model.TTSRenderer) *TTSRendererResponse {
	return &TTSRendererResponse{
		ID:          renderer.ID,
		Name:        renderer.Name,
		Provider:    renderer.Provider,
		Description: renderer.Description,
		CreatedAt:   renderer.CreatedAt,
		UpdatedAt:   renderer.UpdatedAt,
	}
}

// NewTTSRendererDetailResponse builds the edit-safe renderer representation.
func NewTTSRendererDetailResponse(renderer *model.TTSRenderer, config map[string]interface{}, secrets []string) *TTSRendererDetailResponse {
	return &TTSRendererDetailResponse{
		TTSRendererResponse:    *NewTTSRendererResponse(renderer),
		ConnectionConfig:       config,
		ConfiguredSecretFields: secrets,
	}
}
