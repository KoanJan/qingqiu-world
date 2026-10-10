package handler

import (
	"fmt"
	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/agent"
	"qingqiu-world-server/internal/service/runtime"
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
		req.LLMConfigID, req.Avatar,
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
	agent.Refresh(person.ID)

	// Deliver the agent's origin record as a self-orienting biography event.
	runtime.SendBiographyEvent(entity.ID, person.ID)

	response.Success(c, schema.NewAgentResponse(entity, person))
}

// ListAgents handles listing all agents.
func (h *Handler) ListAgents(c *gin.Context) {
	skip, limit := getPagination(c)
	var entities []model.AgentConfig
	err := database.DB.Where("person_id IN (SELECT id FROM persons WHERE status = ?)", model.PersonStatusActive).
		Offset(skip).Limit(limit).Find(&entities).Error
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
	}
	if err = dops.UpdateAIPerson(aiPersonUpdates); err != nil {
		applogger.Error("UpdateAIPerson: transaction failed", "person_id", personID, "error", err)
		response.InternalError(c, "Failed to update ai person")
		return
	}
	agent.Refresh(personID)

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

// DeleteAgent ends an agent's activity while preserving its identity and history.
// The ID in the URL is the person_id (frontend's "agent id").
func (h *Handler) DeleteAgent(c *gin.Context) {
	personID := getPathID(c)
	_, err := dops.GetPerson(personID)
	if err != nil {
		handleNotFound(c, "Person", personID)
		return
	}

	ac, err := dops.GetAgentConfigByPersonID(personID)
	if err != nil {
		handleNotFound(c, "AgentConfig", personID)
		return
	}
	changed, err := runtime.DeceaseAgent(ac.ID, personID)
	if err != nil {
		applogger.Error("DeleteAgent: failed to mark person deceased", "person_id", personID, "error", err)
		response.InternalError(c, "Failed to end agent activity")
		return
	}
	agent.Refresh(personID)

	if changed {
		response.SuccessMessage(c, "Agent activity ended", nil)
	} else {
		response.SuccessMessage(c, "Agent already deceased", nil)
	}
}
