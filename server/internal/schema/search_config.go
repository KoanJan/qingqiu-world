package schema

import (
	"time"

	"qingqiu-world-server/internal/model"
)

// SearchConfigUpdate contains the mutable fields for updating a search config.
type SearchConfigUpdate struct {
	Provider    *string `json:"provider"`
	APIKey      *string `json:"api_key"`
	Description *string `json:"description"`
	IsActive    *bool   `json:"is_active"`
}

// SearchConfigResponse represents the API response for a search config.
type SearchConfigResponse struct {
	ID          int64     `json:"id"`
	Provider    string    `json:"provider"`
	APIKey      string    `json:"api_key"`
	Description string    `json:"description"`
	IsActive    bool      `json:"is_active"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// NewSearchConfigResponse converts a model.SearchConfig to a SearchConfigResponse.
func NewSearchConfigResponse(m *model.SearchConfig) *SearchConfigResponse {
	return &SearchConfigResponse{
		ID:          m.ID,
		Provider:    m.Provider,
		APIKey:      m.APIKey,
		Description: m.Description,
		IsActive:    m.IsActive,
		UpdatedAt:   m.UpdatedAt,
	}
}
