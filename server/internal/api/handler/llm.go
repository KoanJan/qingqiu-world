package handler

import (
	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// CreateLLMConfig handles creating a new LLM configuration.
func (h *Handler) CreateLLMConfig(c *gin.Context) {
	var req schema.LLMConfigCreate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	entity := model.LLMConfig{
		Name:        req.Name,
		ModelID:     req.ModelID,
		BaseURL:     req.BaseURL,
		APIKey:      req.APIKey,
		Description: derefString(req.Description),
	}
	if err := dops.CreateLLMConfig(&entity); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, schema.NewLLMConfigResponse(&entity))
}

// ListLLMConfigs handles listing all LLM configurations.
func (h *Handler) ListLLMConfigs(c *gin.Context) {
	skip, limit := getPagination(c)
	entities, err := dops.GetMulti[model.LLMConfig](skip, limit)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, schema.NewLLMConfigResponseList(entities))
}

// GetLLMConfig handles retrieving a single LLM configuration by ID.
func (h *Handler) GetLLMConfig(c *gin.Context) {
	id := getPathID(c)
	entity, err := dops.Get[model.LLMConfig](id)
	if err != nil {
		handleNotFound(c, "LLM config", id)
		return
	}
	response.Success(c, schema.NewLLMConfigResponse(entity))
}

// UpdateLLMConfig handles updating an existing LLM configuration.
func (h *Handler) UpdateLLMConfig(c *gin.Context) {
	id := getPathID(c)
	entity, err := dops.Get[model.LLMConfig](id)
	if err != nil {
		handleNotFound(c, "LLM config", id)
		return
	}
	var req schema.LLMConfigUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	updates := req.BuildUpdates()
	if len(updates) > 0 {
		dops.Update(entity, updates)
		if entity, err = dops.GetLLMConfig(id); err != nil {
			applogger.Error("failed to refresh LLM config after update", "id", id, "error", err)
		}
	}
	response.Success(c, schema.NewLLMConfigResponse(entity))
}

// DeleteLLMConfig handles deleting an LLM configuration.
func (h *Handler) DeleteLLMConfig(c *gin.Context) {
	id := getPathID(c)
	_, err := dops.Get[model.LLMConfig](id)
	if err != nil {
		handleNotFound(c, "LLM config", id)
		return
	}

	referencingAgents, err := dops.ListAgentConfigsByLLMConfigID(id)
	if err != nil {
		applogger.Error("failed to check referencing agents for LLM config", "id", id, "error", err)
	}
	if len(referencingAgents) > 0 {
		names := make([]string, len(referencingAgents))
		for i, a := range referencingAgents {
			names[i] = dops.GetAgentConfigName(a.ID)
		}
		response.BadRequest(c, "Cannot delete LLM config: it is referenced by "+strconv.Itoa(len(referencingAgents))+" agent(s): "+strings.Join(names, ", "))
		return
	}
	// Check if this LLM config is set as the system LLM.
	sysCfg := dops.GetSystemLLMConfig()
	if sysCfg != nil && sysCfg.ID == id {
		response.BadRequest(c, "Cannot delete LLM config: it is currently set as the system LLM")
		return
	}

	dops.Delete[model.LLMConfig](id)
	response.SuccessMessage(c, "LLM config deleted successfully", nil)
}

// GetSystemLLMConfigHandler returns the current system-level LLM config.
// GET /api/public-experiences/system-llm-config
func (h *Handler) GetSystemLLMConfigHandler(c *gin.Context) {
	cfg := dops.GetSystemLLMConfig()
	if cfg == nil {
		response.Success(c, gin.H{})
		return
	}
	response.Success(c, gin.H{
		"llm_config_id": cfg.ID,
		"name":          cfg.Name,
		"model_id":      cfg.ModelID,
	})
}

// UpdateSystemLLMConfigHandler updates the system-level LLM config.
// PUT /api/public-experiences/system-llm-config
func (h *Handler) UpdateSystemLLMConfigHandler(c *gin.Context) {
	var req struct {
		LLMConfigID int64 `json:"llm_config_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	// Validate that the referenced LLM config exists
	if _, err := dops.GetLLMConfig(req.LLMConfigID); err != nil {
		response.BadRequest(c, "LLM config not found")
		return
	}

	if err := dops.UpdateSystemLLMConfig(req.LLMConfigID); err != nil {
		applogger.Error("Failed to update system LLM config", "error", err)
		response.InternalError(c, "Failed to update system LLM config")
		return
	}

	cfg := dops.GetSystemLLMConfig()
	if cfg == nil {
		response.Success(c, gin.H{})
		return
	}
	response.Success(c, gin.H{
		"llm_config_id": cfg.ID,
		"name":          cfg.Name,
		"model_id":      cfg.ModelID,
	})
}
