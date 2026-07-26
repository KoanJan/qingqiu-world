package handler

import (
	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/experience"

	"github.com/gin-gonic/gin"
)

// ListPublicExperiences returns a paginated list of all public experiences.
// GET /api/public-experiences
func (h *Handler) ListPublicExperiences(c *gin.Context) {
	skip, limit := getPagination(c)

	entities, err := dops.ListPublicExperiences(skip, limit)
	if err != nil {
		applogger.Error("failed to list public experiences", "error", err)
		response.InternalError(c, "Failed to list public experiences")
		return
	}

	response.Success(c, schema.NewPublicExperienceResponseList(entities))
}

// GetPublicExperience returns a single public experience by ID.
// GET /api/public-experiences/:id
func (h *Handler) GetPublicExperience(c *gin.Context) {
	id := getPathID(c)

	entity, err := dops.Get[model.PublicExperience](id)
	if err != nil {
		handleNotFound(c, "Public experience", id)
		return
	}

	response.Success(c, schema.NewPublicExperienceResponse(entity))
}

// DeletePublicExperience deletes a public experience by ID.
// DELETE /api/public-experiences/:id
func (h *Handler) DeletePublicExperience(c *gin.Context) {
	id := getPathID(c)

	entity, err := dops.Get[model.PublicExperience](id)
	if err != nil {
		handleNotFound(c, "Public experience", id)
		return
	}

	// Delete the vector first (1:1 relationship, experience_id is the PK)
	if err := dops.DeletePublicExperienceVectors(id); err != nil {
		applogger.Error("DeletePublicExperience: failed to delete vector", "id", id, "error", err)
	}

	if err := dops.Delete[model.PublicExperience](entity.ID); err != nil {
		applogger.Error("DeletePublicExperience: failed to delete experience", "id", id, "error", err)
		response.InternalError(c, "Failed to delete public experience")
		return
	}

	applogger.Info("PublicExperience deleted", "id", id)
	response.SuccessMessage(c, "Public experience deleted successfully", nil)
}

// IngestPublicExperience submits a skill for async refinement.
// POST /api/public-experiences/ingest
func (h *Handler) IngestPublicExperience(c *gin.Context) {
	var req schema.PublicExperienceIngest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	uploaded, err := experience.IngestSkill(c.Request.Context(), experience.IngestSkillParams{
		FileName:   req.FileName,
		RawContent: req.RawContent,
	})
	if err != nil {
		applogger.Error("IngestPublicExperience failed", "file_name", req.FileName, "error", err)
		response.InternalError(c, "Failed to submit skill: "+err.Error())
		return
	}

	response.Success(c, schema.NewUploadedSkillResponse(uploaded))
}

// RedistillPublicExperience re-triggers LLM distillation for a public experience.
// POST /api/public-experiences/:id/redistill
func (h *Handler) RedistillPublicExperience(c *gin.Context) {
	id := getPathID(c)

	if err := experience.RedistillPublicExperience(c.Request.Context(), id); err != nil {
		applogger.Error("RedistillPublicExperience failed", "id", id, "error", err)
		response.InternalError(c, "Failed to re-distill: "+err.Error())
		return
	}

	response.SuccessMessage(c, "Re-distillation started", nil)
}
