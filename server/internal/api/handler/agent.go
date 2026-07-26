package handler

import (
	"fmt"
	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/runtime"
	"qingqiu-world-server/internal/service/workspace"
	"strings"

	"github.com/gin-gonic/gin"
)

// CreateAgent handles creating a new agent.
func (h *Handler) CreateAgent(c *gin.Context) {
	var req schema.AgentCreate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	entity, person, err := dops.CreateAIPerson(
		req.Name, req.Description, req.CharacterSettings,
		req.LLMConfigID, req.Avatar, req.KnowledgeBaseIDs,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			response.BadRequest(c, fmt.Sprintf("Agent name '%s' already exists", req.Name))
			return
		}
		response.InternalError(c, err.Error())
		return
	}

	// Register and start the agent's runtime so it can receive events immediately.
	runtime.StartRuntime(entity.ID)

	response.Success(c, schema.NewAgentResponse(entity, person))
}

// ListAgents handles listing all agents.
func (h *Handler) ListAgents(c *gin.Context) {
	skip, limit := getPagination(c)
	entities, err := dops.GetMulti[model.AgentConfig](skip, limit)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	// Load all associated persons for names and bios
	personsMap := loadAgentConfigPersons(entities)
	response.Success(c, schema.NewAgentResponseList(entities, personsMap))
}

// GetAgent handles retrieving a single agent config by ID.
// The ID in the URL is the person_id (frontend's "agent id").
func (h *Handler) GetAgent(c *gin.Context) {
	personID := getPathID(c)
	ac, err := dops.GetAgentConfigByPersonID(personID)
	if err != nil {
		handleNotFound(c, "AgentConfig", personID)
		return
	}
	person, err := dops.GetPerson(personID)
	if err != nil {
		applogger.Error("GetAgentConfig: person not found", "person_id", personID, "error", err)
		response.InternalError(c, "Person not found")
		return
	}
	response.Success(c, schema.NewAgentResponse(ac, person))
}

// UpdateAgent handles updating an existing agent config.
// The ID in the URL is the person_id (frontend's "agent id").
func (h *Handler) UpdateAgent(c *gin.Context) {
	personID := getPathID(c)
	_, err := dops.GetAgentConfigByPersonID(personID)
	if err != nil {
		handleNotFound(c, "AgentConfig", personID)
		return
	}
	var req schema.AgentUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	// Wrap agent + person updates in a transaction
	aiPersonUpdates := &dops.AIPersonUpdates{
		PersonID:          personID,
		Bio:               req.Bio,
		CharacterSettings: req.CharacterSettings,
		LLMConfigID:       req.LLMConfigID,
		Avatar:            req.Avatar,
		KnowledgeBaseIDs:  req.KnowledgeBaseIDs,
	}
	if err = dops.UpdateAIPerson(aiPersonUpdates); err != nil {
		applogger.Error("UpdateAIPerson: transaction failed", "person_id", personID, "error", err)
		response.InternalError(c, "Failed to update ai person")
		return
	}

	// Reload for response
	ac, err := dops.GetAgentConfigByPersonID(personID)
	if err != nil {
		applogger.Error("UpdateAIPerson: failed to reload agent config", "person_id", personID, "error", err)
		response.InternalError(c, "Failed to reload agent config")
		return
	}
	person, err := dops.GetPerson(personID)
	if err != nil {
		applogger.Error("UpdateAIPerson: failed to reload person", "person_id", personID, "error", err)
		response.InternalError(c, "Failed to reload person")
		return
	}
	response.Success(c, schema.NewAgentResponse(ac, person))
}

// DeleteAgent handles deleting an agent config and its resources.
// The ID in the URL is the person_id (frontend's "agent id").
func (h *Handler) DeleteAgent(c *gin.Context) {
	personID := getPathID(c)
	person, err := dops.GetPerson(personID)
	if err != nil {
		handleNotFound(c, "Person", personID)
		return
	}

	if person.Avatar != "" {
		avatarPath := getAvatarsDir() + "/" + person.Avatar
		osRemoveIfExists(avatarPath)
	}

	sessionIDs, err := dops.DeleteAIPersonCascade(personID)
	if err != nil {
		applogger.Error("DeleteAgentConfig: cascade delete failed", "person_id", personID, "error", err)
		response.InternalError(c, "Failed to delete agent config")
		return
	}

	// Filesystem cleanup (not transactional)
	for _, sid := range sessionIDs {
		workspace.RemoveWorkspace(personID, sid)
		workspace.RemoveAac(personID, sid)
	}

	response.SuccessMessage(c, "Agent config deleted successfully", nil)
}
