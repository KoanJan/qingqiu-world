package handler

import (
	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/energy"

	"github.com/gin-gonic/gin"
)

// ListAgentsBrief returns all AI agents with their current energy for the
// sidebar agent list. Energy comes from agent_states (by person_id); agents
// without a row default to 100.
func (h *Handler) ListAgentsBrief(c *gin.Context) {
	persons, err := dops.GetAIPersons()
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	if len(persons) == 0 {
		response.Success(c, []schema.AgentBrief{})
		return
	}

	personIDs := make([]int64, len(persons))
	for i, p := range persons {
		personIDs[i] = p.ID
	}
	stateMap := energy.LoadStates(personIDs)

	result := make([]schema.AgentBrief, 0, len(persons))
	for _, p := range persons {
		e := 100
		if s, ok := stateMap[p.ID]; ok {
			e = s.Energy
		}
		result = append(result, schema.AgentBrief{
			ID:     p.ID,
			Name:   p.Name,
			Avatar: p.Avatar,
			Energy: e,
		})
	}

	response.Success(c, result)
}
