package schema

import (
	"time"

	"qingqiu-world-server/internal/model"
)

// EmbeddingConfigBase contains the common fields for embedding config creation and updates.
type EmbeddingConfigBase struct {
	Name        string `json:"name" binding:"required"`
	ModelID     string `json:"model_id" binding:"required"`
	BaseURL     string `json:"base_url" binding:"required"`
	APIKey      string `json:"api_key" binding:"required"`
	Description string `json:"description"`
}

// EmbeddingConfigCreate is an alias of EmbeddingConfigBase for creating embedding configs.
type EmbeddingConfigCreate EmbeddingConfigBase

// EmbeddingConfigUpdate contains the mutable fields for updating an embedding config.
type EmbeddingConfigUpdate struct {
	Name        *string `json:"name"`
	ModelID     *string `json:"model_id"`
	BaseURL     *string `json:"base_url"`
	APIKey      *string `json:"api_key"`
	Description *string `json:"description"`
}

// EmbeddingConfigResponse represents the API response for an embedding config.
type EmbeddingConfigResponse struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	ModelID     string    `json:"model_id"`
	BaseURL     string    `json:"base_url"`
	APIKey      string    `json:"api_key"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// NewEmbeddingConfigResponse converts a model.EmbeddingConfig to an EmbeddingConfigResponse.
func NewEmbeddingConfigResponse(m *model.EmbeddingConfig) *EmbeddingConfigResponse {
	return &EmbeddingConfigResponse{
		ID:          m.ID,
		Name:        m.Name,
		ModelID:     m.ModelID,
		BaseURL:     m.BaseURL,
		APIKey:      m.APIKey,
		Description: m.Description,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

// NewEmbeddingConfigResponseList converts a list of model.EmbeddingConfig to EmbeddingConfigResponse list.
func NewEmbeddingConfigResponseList(entities []model.EmbeddingConfig) []*EmbeddingConfigResponse {
	result := make([]*EmbeddingConfigResponse, 0, len(entities))
	for i := range entities {
		result = append(result, NewEmbeddingConfigResponse(&entities[i]))
	}
	return result
}

// BuildUpdates builds a map of non-nil update fields from EmbeddingConfigUpdate.
func (req *EmbeddingConfigUpdate) BuildUpdates() map[string]interface{} {
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
