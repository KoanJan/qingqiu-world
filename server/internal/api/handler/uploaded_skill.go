package handler

import (
	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"

	"github.com/gin-gonic/gin"
)

// GetUploadedSkill returns a single uploaded skill by ID.
// GET /api/uploaded-skills/:id
func (h *Handler) GetUploadedSkill(c *gin.Context) {
	id := getPathID(c)

	entity, err := dops.Get[model.UploadedSkill](id)
	if err != nil {
		handleNotFound(c, "Uploaded skill", id)
		return
	}

	response.Success(c, schema.NewUploadedSkillResponse(entity))
}
