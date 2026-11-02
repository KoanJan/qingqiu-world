package handler

import (
	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/agent"
	"qingqiu-world-server/internal/service/llm"
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
		ProbeStatus: model.ProbeStatusUnverified,
	}
	entity.ProbeConfigHash = entity.ProbeFingerprint()
	if err := dops.CreateLLMConfig(&entity); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, schema.NewLLMConfigResponse(probeAndLoadLLMConfig(c, &entity)))
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
	probeNeeded := false
	if len(updates) > 0 {
		connectionChanged := false
		if value, ok := updates["model_id"]; ok {
			connectionChanged = connectionChanged || entity.ModelID != value.(string)
			entity.ModelID = value.(string)
		}
		if value, ok := updates["base_url"]; ok {
			connectionChanged = connectionChanged || entity.BaseURL != value.(string)
			entity.BaseURL = value.(string)
		}
		if value, ok := updates["api_key"]; ok {
			connectionChanged = connectionChanged || entity.APIKey != value.(string)
			entity.APIKey = value.(string)
		}
		if connectionChanged {
			probeNeeded = true
			updates["probe_config_hash"] = entity.ProbeFingerprint()
			updates["probe_status"] = model.ProbeStatusUnverified
			updates["probe_required_tools_ok"] = false
			updates["probe_required_tools_error"] = ""
			updates["probe_structured_output_ok"] = false
			updates["probe_structured_output_error"] = ""
			updates["probe_checked_at"] = int64(0)
		}
		if err := database.DB.Model(&model.LLMConfig{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			response.InternalError(c, "Failed to update LLM config")
			return
		}
		if connectionChanged {
			// Agents keep their assignment but must use the edited connection.
			if refs, err := dops.ListAgentConfigsByLLMConfigID(id); err == nil {
				for _, ac := range refs {
					agent.Refresh(ac.PersonID)
				}
			} else {
				applogger.Error("failed to refresh agents after LLM connection edit", "llm_config_id", id, "error", err)
			}
		}
		entity, err = dops.GetLLMConfig(id)
		if err != nil {
			response.InternalError(c, "Failed to load LLM config after update")
			return
		}
	}
	if probeNeeded {
		entity = probeAndLoadLLMConfig(c, entity)
	}
	response.Success(c, schema.NewLLMConfigResponse(entity))
}

// ProbeLLMConfig repeats the live protocol check after a transient failure or repair.
func (h *Handler) ProbeLLMConfig(c *gin.Context) {
	id := getPathID(c)
	entity, err := dops.GetLLMConfig(id)
	if err != nil {
		handleNotFound(c, "LLM config", id)
		return
	}
	response.Success(c, schema.NewLLMConfigResponse(probeAndLoadLLMConfig(c, entity)))
}

func probeAndLoadLLMConfig(c *gin.Context, entity *model.LLMConfig) *model.LLMConfig {
	fingerprint := entity.ProbeFingerprint()
	if entity.ProbeConfigHash != fingerprint {
		claim := database.DB.Model(&model.LLMConfig{}).
			Where("id = ? AND probe_config_hash = ?", entity.ID, entity.ProbeConfigHash).
			UpdateColumns(map[string]interface{}{"probe_config_hash": fingerprint, "probe_status": model.ProbeStatusUnverified})
		if claim.Error != nil || claim.RowsAffected == 0 {
			applogger.Error("failed to prepare LLM config probe", "id", entity.ID, "error", claim.Error)
			refreshed, err := dops.GetLLMConfig(entity.ID)
			if err == nil {
				return refreshed
			}
			return entity
		}
	}
	result := llm.Probe(c.Request.Context(), entity.BaseURL, entity.APIKey, entity.ModelID)
	status := model.ProbeStatusFailed
	if result.RequiredTools.OK && result.StructuredOutput.OK {
		status = model.ProbeStatusPassed
	} else {
		applogger.Error("LLM compatibility probe failed", "llm_config_id", entity.ID,
			"required_tools_error", result.RequiredTools.Error, "structured_output_error", result.StructuredOutput.Error)
	}
	updates := map[string]interface{}{
		"probe_status":                  status,
		"probe_required_tools_ok":       result.RequiredTools.OK,
		"probe_required_tools_error":    result.RequiredTools.Error,
		"probe_structured_output_ok":    result.StructuredOutput.OK,
		"probe_structured_output_error": result.StructuredOutput.Error,
		"probe_checked_at":              result.CheckedAt.UnixMilli(),
	}
	query := database.DB.Model(&model.LLMConfig{}).Where("id = ? AND probe_config_hash = ?", entity.ID, fingerprint).UpdateColumns(updates)
	if query.Error != nil {
		applogger.Error("failed to persist LLM probe result", "id", entity.ID, "error", query.Error)
	}
	refreshed, err := dops.GetLLMConfig(entity.ID)
	if err != nil {
		applogger.Error("failed to reload LLM config after probe", "id", entity.ID, "error", err)
		return entity
	}
	return refreshed
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
