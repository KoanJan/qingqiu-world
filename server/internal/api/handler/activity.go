package handler

import (
	"github.com/gin-gonic/gin"

	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/service/task"
)

// GetSessionActivities returns a flat activity timeline for all task works in a session.
//
// GET /api/sessions/:id/activities
//
// Queries works of type=2 (Task) for the session, collects all their interactions,
// and converts them into a flat timeline of human-readable activity events.
func (h *Handler) GetSessionActivities(c *gin.Context) {
	sessionID := getPathID(c)

	// Verify session exists
	_, err := dops.GetSession(sessionID)
	if err != nil {
		response.NotFound(c, "Session not found")
		return
	}

	// Collect all task work IDs for this session
	workIDs, err := dops.ListTaskWorks(sessionID)
	if err != nil {
		applogger.Error("GetSessionActivities: failed to query works",
			"session_id", sessionID, "error", err)
		response.InternalError(c, "Failed to query activities")
		return
	}

	if len(workIDs) == 0 {
		response.Success(c, []struct{}{})
		return
	}

	// Query all interactions across all task works, ordered by creation time
	interactions, err := dops.ListInteractions(workIDs)
	if err != nil {
		applogger.Error("GetSessionActivities: failed to query interactions",
			"session_id", sessionID, "error", err)
		response.InternalError(c, "Failed to query activities")
		return
	}

	// Get the agent participant for this session by joining persons table (type=1=AI)
	personIDs, err := dops.GetSessionAIParticipantIDs(sessionID)
	if err != nil {
		applogger.Error("GetSessionActivities: failed to query agent participant", "session_id", sessionID, "error", err)
		response.InternalError(c, "Failed to query activities")
		return
	}
	agentPersonID := personIDs[0]

	// Find the actual agent config ID from the person_id
	_, err = dops.GetAgentConfigByPersonID(agentPersonID)
	if err != nil {
		applogger.Error("GetSessionActivities: failed to find agent for person",
			"person_id", agentPersonID, "error", err)
		response.InternalError(c, "Failed to query activities")
	}

	// Build flat timeline of events and inject person_id as agent_id for frontend.
	events := task.BuildActivityEvents(interactions)
	for i := range events {
		events[i].PersonID = agentPersonID
	}
	response.Success(c, events)
}
