package handler

import (
	"encoding/json"
	"errors"

	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	renderercore "qingqiu-world-server/internal/service/speech/renderer"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ListTTSProviders returns dynamic provider configuration contracts.
func (h *Handler) ListTTSProviders(c *gin.Context) {
	definitions, err := dops.ListTTSProviderDefinitions()
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	items := make([]*schema.TTSProviderDefinitionResponse, 0, len(definitions))
	for i := range definitions {
		items = append(items, schema.NewTTSProviderDefinitionResponse(&definitions[i]))
	}
	response.Success(c, items)
}

// CreateTTSRenderer creates one explicitly selected provider connection.
func (h *Handler) CreateTTSRenderer(c *gin.Context) {
	var req schema.TTSRendererCreate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	var connectionConfig map[string]interface{}
	if !json.Valid(req.ConnectionConfig) || json.Unmarshal(req.ConnectionConfig, &connectionConfig) != nil || connectionConfig == nil {
		response.BadRequest(c, "connection_config must be a JSON object")
		return
	}
	renderer := model.TTSRenderer{
		Name:                 req.Name,
		Provider:             req.Provider,
		ConnectionConfigJSON: string(req.ConnectionConfig),
		Description:          req.Description,
	}
	if err := renderercore.Validate(c.Request.Context(), renderer); err != nil {
		applogger.Warn("create TTS renderer rejected: invalid connection config", "provider", renderer.Provider, "error", err)
		response.BadRequest(c, err.Error())
		return
	}
	if err := dops.Create(&renderer); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, schema.NewTTSRendererResponse(&renderer))
}

// ListTTSRenderers returns configured TTS connections.
func (h *Handler) ListTTSRenderers(c *gin.Context) {
	renderers, err := dops.ListTTSRenderers()
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	items := make([]*schema.TTSRendererResponse, 0, len(renderers))
	for i := range renderers {
		items = append(items, schema.NewTTSRendererResponse(&renderers[i]))
	}
	response.Success(c, items)
}

// GetTTSRenderer returns the editable, secret-free renderer detail.
func (h *Handler) GetTTSRenderer(c *gin.Context) {
	id := getPathID(c)
	renderer, err := dops.GetTTSRenderer(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			handleNotFound(c, "TTS renderer", id)
			return
		}
		applogger.Error("load TTS renderer failed", "id", id, "error", err)
		response.InternalError(c, err.Error())
		return
	}
	definition, err := dops.GetTTSProviderDefinition(renderer.Provider)
	if err != nil {
		applogger.Error("load TTS provider definition for renderer", "id", id, "provider", renderer.Provider, "error", err)
		response.InternalError(c, err.Error())
		return
	}
	config, secrets, err := renderercore.PublicConnectionConfig(*definition, renderer.ConnectionConfigJSON)
	if err != nil {
		applogger.Error("build public TTS renderer configuration", "id", id, "error", err)
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, schema.NewTTSRendererDetailResponse(renderer, config, secrets))
}

// UpdateTTSRenderer updates one provider connection while preserving omitted
// write-only credentials. Its provider cannot be changed.
func (h *Handler) UpdateTTSRenderer(c *gin.Context) {
	id := getPathID(c)
	renderer, err := dops.GetTTSRenderer(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			handleNotFound(c, "TTS renderer", id)
			return
		}
		applogger.Error("load TTS renderer before update failed", "id", id, "error", err)
		response.InternalError(c, err.Error())
		return
	}
	var req schema.TTSRendererUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if !json.Valid(req.ConnectionConfig) {
		response.BadRequest(c, "connection_config must be a JSON object")
		return
	}
	definition, err := dops.GetTTSProviderDefinition(renderer.Provider)
	if err != nil {
		applogger.Error("load TTS provider definition before renderer update", "id", id, "error", err)
		response.InternalError(c, err.Error())
		return
	}
	merged, err := renderercore.MergeConnectionConfig(*definition, renderer.ConnectionConfigJSON, string(req.ConnectionConfig))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	candidate := *renderer
	candidate.Name = req.Name
	candidate.Description = req.Description
	candidate.ConnectionConfigJSON = merged
	if err := renderercore.Validate(c.Request.Context(), candidate); err != nil {
		applogger.Warn("update TTS renderer rejected", "id", id, "provider", renderer.Provider, "error", err)
		response.BadRequest(c, err.Error())
		return
	}
	if err := dops.Update(renderer, map[string]interface{}{
		"name":                   candidate.Name,
		"description":            candidate.Description,
		"connection_config_json": candidate.ConnectionConfigJSON,
	}); err != nil {
		applogger.Error("update TTS renderer failed", "id", id, "error", err)
		response.InternalError(c, err.Error())
		return
	}
	updated, err := dops.GetTTSRenderer(id)
	if err != nil {
		applogger.Error("reload TTS renderer after update failed", "id", id, "error", err)
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, schema.NewTTSRendererResponse(updated))
}

// DeleteTTSRenderer deletes an unbound connection. Only current Agent voice
// versions participate in the application-level reference constraint.
func (h *Handler) DeleteTTSRenderer(c *gin.Context) {
	id := getPathID(c)
	if _, err := dops.GetTTSRenderer(id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			handleNotFound(c, "TTS renderer", id)
			return
		}
		applogger.Error("load TTS renderer before deletion failed", "id", id, "error", err)
		response.InternalError(c, err.Error())
		return
	}
	count, err := dops.DeleteTTSRendererIfUnbound(id)
	if err != nil {
		applogger.Error("delete unbound TTS renderer failed", "id", id, "error", err)
		response.InternalError(c, err.Error())
		return
	}
	if count > 0 {
		applogger.Warn("delete TTS renderer rejected because it is bound", "id", id, "agent_count", count)
		response.BadRequest(c, "Cannot delete TTS renderer: it is bound to a current Agent voice")
		return
	}
	response.SuccessMessage(c, "TTS renderer deleted successfully", nil)
}
