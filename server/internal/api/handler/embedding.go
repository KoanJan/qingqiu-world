package handler

import (
	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"

	"github.com/gin-gonic/gin"
)

// GetEmbeddingConfig returns the global embedding configuration.
// Returns nil fields (zero values) if no config exists.
func (h *Handler) GetEmbeddingConfig(c *gin.Context) {
	config := dops.GetEmbeddingConfig()
	if config == nil {
		// Return empty config so the UI can show an empty form for initial setup
		response.Success(c, schema.EmbeddingConfigResponse{})
		return
	}
	response.Success(c, schema.NewEmbeddingConfigResponse(config))
}

// UpdateEmbeddingConfig updates the global embedding configuration (upsert).
func (h *Handler) UpdateEmbeddingConfig(c *gin.Context) {
	var req schema.EmbeddingConfigUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	config := dops.GetEmbeddingConfig()
	if config == nil {
		// Create new config
		entity := model.EmbeddingConfig{
			Name:        derefString(req.Name),
			ModelID:     derefString(req.ModelID),
			BaseURL:     derefString(req.BaseURL),
			APIKey:      derefString(req.APIKey),
			Description: derefString(req.Description),
		}
		config = dops.UpdateEmbeddingConfig(entity)
	} else {
		entity := *config
		if req.Name != nil {
			entity.Name = *req.Name
		}
		if req.ModelID != nil {
			entity.ModelID = *req.ModelID
		}
		if req.BaseURL != nil {
			entity.BaseURL = *req.BaseURL
		}
		if req.APIKey != nil {
			entity.APIKey = *req.APIKey
		}
		if req.Description != nil {
			entity.Description = *req.Description
		}
		config = dops.UpdateEmbeddingConfig(entity)
	}

	if config == nil {
		response.InternalError(c, "Failed to update embedding config")
		return
	}
	response.Success(c, schema.NewEmbeddingConfigResponse(config))
}
