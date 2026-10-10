package handler

import (
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"

	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/focusedwork"
)

// ListActivityAgents exposes historical agent identities for the Focus browser.
func (h *Handler) ListActivityAgents(c *gin.Context) {
	agents, err := dops.ListActivityAgents()
	if err != nil {
		applogger.Error("ListActivityAgents: failed to query agents", "error", err)
		response.InternalError(c, "Failed to query activity agents")
		return
	}
	response.Success(c, agents)
}

// ListActivityWorkspaces returns one page of an agent's workspaces.
func (h *Handler) ListActivityWorkspaces(c *gin.Context) {
	personID := getPathID(c)
	if _, err := dops.GetAgentConfigByPersonID(personID); err != nil {
		response.NotFound(c, "Agent not found")
		return
	}
	page, err := activityPositiveParam(c, "page", 1)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	workspaces, hasMore, err := dops.ListActivityWorkspaces(personID, page, 50)
	if err != nil {
		applogger.Error("ListActivityWorkspaces: query failed", "person_id", personID, "error", err)
		response.InternalError(c, "Failed to query workspaces")
		return
	}
	response.Success(c, gin.H{"workspaces": workspaces, "has_more": hasMore})
}

// ListActivityWorks returns Focus works linked to one owned workspace.
func (h *Handler) ListActivityWorks(c *gin.Context) {
	personID := getPathID(c)
	workspaceID, err := activityPositiveParam(c, "workspace_id", 0)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if _, err := dops.GetOwnedWorkspace(personID, int64(workspaceID)); err != nil {
		response.NotFound(c, "Workspace not found")
		return
	}
	beforeWorkID := 0
	if c.Query("before_work_id") != "" {
		beforeWorkID, err = activityPositiveParam(c, "before_work_id", 0)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
	}
	works, hasMore, err := dops.ListActivityWorks(personID, int64(workspaceID), int64(beforeWorkID), 30)
	if err != nil {
		applogger.Error("ListActivityWorks: query failed", "person_id", personID, "workspace_id", workspaceID, "error", err)
		response.InternalError(c, "Failed to query works")
		return
	}
	for _, work := range works {
		if work.DefaultWorkspaceID <= 0 {
			applogger.Error("ListActivityWorks: Focus work has no default workspace", "person_id", personID, "work_id", work.ID)
			response.InternalError(c, "Focus work has no default workspace")
			return
		}
	}
	nextBefore := int64(0)
	if hasMore && len(works) > 0 {
		nextBefore = works[len(works)-1].ID
	}
	response.Success(c, gin.H{"works": works, "has_more": hasMore, "next_before_work_id": nextBefore})
}

// GetWorkActivities returns one interaction page for an owned Focus work.
func (h *Handler) GetWorkActivities(c *gin.Context) {
	personID := getPathID(c)
	workID, err := activityPositiveParam(c, "work_id", 0)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if _, err := dops.GetOwnedActivityWork(personID, int64(workID)); err != nil {
		response.NotFound(c, "Work not found")
		return
	}
	beforeInteractionID, afterInteractionID, limit, err := activityPageParams(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	interactions, hasMore, err := dops.ListActivityInteractions([]int64{int64(workID)}, beforeInteractionID, afterInteractionID, limit)
	if err != nil {
		applogger.Error("GetWorkActivities: query failed", "person_id", personID, "work_id", workID, "error", err)
		response.InternalError(c, "Failed to query activities")
		return
	}
	page := schema.ActivityPage{Events: focusedwork.BuildActivityEvents(interactions, map[int64]int64{int64(workID): personID}), HasMore: hasMore}
	if len(interactions) > 0 {
		page.NextAfterInteractionID = interactions[len(interactions)-1].ID
	}
	if hasMore && afterInteractionID == 0 && len(interactions) > 0 {
		page.NextBeforeInteractionID = interactions[0].ID
	}
	response.Success(c, page)
}

// activityPositiveParam parses a positive query or path parameter.
func activityPositiveParam(c *gin.Context, name string, fallback int) (int, error) {
	raw := c.Param(name)
	if raw == "" {
		raw = c.Query(name)
	}
	if raw == "" && fallback > 0 {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}
