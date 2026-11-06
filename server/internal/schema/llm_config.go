package schema

import (
	"time"

	"qingqiu-world-server/internal/model"
)

// LLMConfigBase contains the common fields for LLM config creation and updates.
type LLMConfigBase struct {
	Name        string  `json:"name" binding:"required"`
	ModelID     string  `json:"model_id" binding:"required"`
	BaseURL     string  `json:"base_url" binding:"required"`
	APIKey      string  `json:"api_key" binding:"required"`
	Description *string `json:"description"`
}

// LLMConfigCreate is an alias of LLMConfigBase for creating LLM configs.
type LLMConfigCreate LLMConfigBase

// LLMConfigUpdate contains the mutable fields for updating an LLM config.
type LLMConfigUpdate struct {
	Name        *string `json:"name"`
	ModelID     *string `json:"model_id"`
	BaseURL     *string `json:"base_url"`
	APIKey      *string `json:"api_key"`
	Description *string `json:"description"`
}

// LLMConfigResponse represents the API response for an LLM config.
type LLMConfigResponse struct {
	ID          int64            `json:"id"`
	Name        string           `json:"name"`
	ModelID     string           `json:"model_id"`
	BaseURL     string           `json:"base_url"`
	APIKey      string           `json:"api_key"`
	Description string           `json:"description"`
	Probe       LLMProbeResponse `json:"probe"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type LLMProbeCapabilityResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type LLMProbeResponse struct {
	Status           model.ProbeStatus          `json:"status"`
	RequiredTools    LLMProbeCapabilityResponse `json:"required_tools"`
	StructuredOutput LLMProbeCapabilityResponse `json:"structured_output"`
	CheckedAt        *time.Time                 `json:"checked_at"`
}

// NewLLMConfigResponse converts a model.LLMConfig to an LLMConfigResponse.
func NewLLMConfigResponse(m *model.LLMConfig) *LLMConfigResponse {
	return &LLMConfigResponse{
		ID:          m.ID,
		Name:        m.Name,
		ModelID:     m.ModelID,
		BaseURL:     m.BaseURL,
		APIKey:      m.APIKey,
		Description: m.Description,
		Probe:       NewLLMProbeResponse(m),
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

func NewLLMProbeResponse(m *model.LLMConfig) LLMProbeResponse {
	result := LLMProbeResponse{Status: model.ProbeStatusUnverified}
	if m.ProbeConfigHash == "" || m.ProbeConfigHash != m.ProbeFingerprint() || m.ProbeCheckedAt == 0 ||
		m.ProbeStatus == model.ProbeStatusUnverified || (m.ProbeStatus == model.ProbeStatusPassed && !m.ProbePassed()) {
		return result
	}
	result.Status = m.ProbeStatus
	result.RequiredTools = LLMProbeCapabilityResponse{OK: m.ProbeRequiredToolsOK, Error: m.ProbeRequiredToolsError}
	result.StructuredOutput = LLMProbeCapabilityResponse{OK: m.ProbeStructuredOutputOK, Error: m.ProbeStructuredOutputError}
	if m.ProbeCheckedAt > 0 {
		checkedAt := time.UnixMilli(m.ProbeCheckedAt).UTC()
		result.CheckedAt = &checkedAt
	}
	return result
}

// NewLLMConfigResponseList converts a list of model.LLMConfig to LLMConfigResponse list.
func NewLLMConfigResponseList(entities []model.LLMConfig) []*LLMConfigResponse {
	result := make([]*LLMConfigResponse, 0, len(entities))
	for i := range entities {
		result = append(result, NewLLMConfigResponse(&entities[i]))
	}
	return result
}

// BuildUpdates builds a map of non-nil update fields from LLMConfigUpdate.
func (req *LLMConfigUpdate) BuildUpdates() map[string]interface{} {
	updates := make(map[string]interface{})
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.ModelID != nil {
		updates["model_id"] = *req.ModelID
	}
	if req.BaseURL != nil {
		updates["base_url"] = *req.BaseURL
	}
	if req.APIKey != nil {
		updates["api_key"] = *req.APIKey
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	return updates
}
