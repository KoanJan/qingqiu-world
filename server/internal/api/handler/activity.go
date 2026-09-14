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

const (
	activityDefaultPageSize = 100
	activityMaxPageSize     = 200
)

// GetSessionActivities returns one cursor-bounded, chronological page of the
// activity timeline for a session's focused works.
//
// GET /api/sessions/:id/activities?before_interaction_id=<id>&limit=<n>
func (h *Handler) GetSessionActivities(c *gin.Context) {
	sessionID := getPathID(c)
	beforeInteractionID, limit, err := activityPageParams(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	// Verify session exists before querying its related work records.
	if _, err := dops.GetSession(sessionID); err != nil {
		response.NotFound(c, "Session not found")
		return
	}

	// Keep each Work's owner so multi-agent sessions render attribution from
	// the actual producer instead of an arbitrary session participant.
	works, err := dops.ListSessionActivityWorks(sessionID)
	if err != nil {
		applogger.Error("GetSessionActivities: failed to query works",
			"session_id", sessionID, "error", err)
		response.InternalError(c, "Failed to query activities")
		return
	}
	if len(works) == 0 {
		response.Success(c, schema.ActivityPage{Events: []schema.ActivityEvent{}})
		return
	}

	workPersonIDs := make(map[int64]int64, len(works))
	validWorkCount := 0
	for _, work := range works {
		if work.PersonID <= 0 {
			applogger.Error("GetSessionActivities: work has invalid owner",
				"session_id", sessionID, "work_id", work.ID, "person_id", work.PersonID)
			continue
		}
		workPersonIDs[work.ID] = work.PersonID
		validWorkCount++
	}
	if validWorkCount == 0 {
		response.Success(c, schema.ActivityPage{Events: []schema.ActivityEvent{}})
		return
	}

	interactions, hasMore, err := dops.ListSessionActivityInteractions(sessionID, beforeInteractionID, limit)
	if err != nil {
		applogger.Error("GetSessionActivities: failed to query interactions",
			"session_id", sessionID, "before_interaction_id", beforeInteractionID, "limit", limit, "error", err)
		response.InternalError(c, "Failed to query activities")
		return
	}

	page := schema.ActivityPage{
		Events:  focusedwork.BuildActivityEvents(interactions, workPersonIDs),
		HasMore: hasMore,
	}
	if hasMore && len(interactions) > 0 {
		page.NextBeforeInteractionID = interactions[0].ID
	}
	response.Success(c, page)
}

// activityPageParams validates the interaction cursor and page size. The
// limit is deliberately bounded so one Activity request cannot monopolize the
// application's single SQLite connection.
func activityPageParams(c *gin.Context) (int64, int, error) {
	beforeInteractionID := int64(0)
	if rawBefore := c.Query("before_interaction_id"); rawBefore != "" {
		parsed, err := strconv.ParseInt(rawBefore, 10, 64)
		if err != nil || parsed <= 0 {
			return 0, 0, fmt.Errorf("before_interaction_id must be a positive integer")
		}
		beforeInteractionID = parsed
	}

	limit := activityDefaultPageSize
	if rawLimit := c.Query("limit"); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > activityMaxPageSize {
			return 0, 0, fmt.Errorf("limit must be between 1 and %d", activityMaxPageSize)
		}
		limit = parsed
	}
	return beforeInteractionID, limit, nil
}
