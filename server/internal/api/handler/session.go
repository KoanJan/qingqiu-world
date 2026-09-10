package handler

import (
	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/workspace"

	"github.com/gin-gonic/gin"
)

// ListSessions handles listing all sessions.
//
// 0.1.3: Returns ALL sessions in the system (not just the user's), with an
// is_participant flag for each. This lets the frontend show the complete
// social picture — including conversations between agents that the user is
// not part of — while clearly distinguishing which sessions the user can
// actively participate in.
func (h *Handler) ListSessions(c *gin.Context) {
	skip, limit := getPagination(c)
	entities, err := dops.GetMulti[model.Session](skip, limit)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	sids := make([]int64, len(entities))
	for i := range entities {
		sids[i] = entities[i].ID
	}
	personMap, err := dops.GetAIPersonsInSessions(sids)
	if err != nil {
		applogger.Error("failed to resolve AI persons for sessions", "count", len(sids), "error", err)
	}

	// Resolve current user's participation for each session.
	participationMap := map[int64]bool{}
	unreadSet := map[int64]bool{}
	if currentUserID, err := dops.GetCurrentUserPersonID(); err != nil {
		applogger.Error("failed to resolve current user person id for participation check", "error", err)
	} else {
		if pm, err := dops.GetParticipatedSessions(currentUserID, sids); err != nil {
			applogger.Error("failed to resolve user participation in sessions", "error", err)
		} else {
			participationMap = pm
		}
		// Resolve unread status for sessions the user participates in.
		participatedSids := make([]int64, 0, len(participationMap))
		for _, sid := range sids {
			if participationMap[sid] {
				participatedSids = append(participatedSids, sid)
			}
		}
		if us, err := dops.GetUnreadSessions(currentUserID, participatedSids); err != nil {
			applogger.Error("failed to resolve unread sessions", "error", err)
		} else {
			unreadSet = us
		}
	}

	response.Success(c, schema.NewSessionResponseList(entities, personMap, participationMap, unreadSet))
}

// GetSession handles retrieving a single session by ID.
func (h *Handler) GetSession(c *gin.Context) {
	id := getPathID(c)
	entity, err := dops.Get[model.Session](id)
	if err != nil {
		handleNotFound(c, "Session", id)
		return
	}
	sm, err := dops.GetAIPersonInSession(id)
	if err != nil {
		applogger.Error("failed to resolve session person", "session_id", id, "error", err)
	}

	// Resolve current user's participation and unread status.
	isParticipant := false
	hasUnread := false
	if currentUserID, err := dops.GetCurrentUserPersonID(); err != nil {
		applogger.Error("failed to resolve current user person id for participation check", "error", err)
	} else {
		if ok, err := dops.IsParticipant(id, currentUserID); err != nil {
			applogger.Error("failed to resolve user participation in session", "session_id", id, "error", err)
		} else {
			isParticipant = ok
		}
		if isParticipant {
			if us, err := dops.GetUnreadSessions(currentUserID, []int64{id}); err != nil {
				applogger.Error("failed to resolve unread status", "session_id", id, "error", err)
			} else {
				hasUnread = us[id]
			}
		}
	}

	response.Success(c, schema.NewSessionResponse(entity, sm, isParticipant, hasUnread))
}

// MarkSessionRead advances the current user's read marker to the latest
// message in the session. Reading is monotonic and never moves the marker
// backwards when concurrent messages arrive.
func (h *Handler) MarkSessionRead(c *gin.Context) {
	id := getPathID(c)
	if _, err := dops.GetSession(id); err != nil {
		handleNotFound(c, "Session", id)
		return
	}
	personID, err := dops.GetCurrentUserPersonID()
	if err != nil {
		response.BadRequest(c, "No user profile found.")
		return
	}
	latestMessageID, err := dops.GetMaxMessageID(id)
	if err != nil {
		applogger.Error("failed to resolve latest message for read marker", "session_id", id, "error", err)
		response.InternalError(c, "Failed to mark session read")
		return
	}
	if err := dops.AdvanceLastReadMessageID(id, personID, latestMessageID); err != nil {
		applogger.Error("failed to advance session read marker", "session_id", id, "error", err)
		response.InternalError(c, "Failed to mark session read")
		return
	}
	response.Success(c, gin.H{"session_id": id, "last_read_message_id": latestMessageID})
}

// UpdateSession handles updating an existing session.
func (h *Handler) UpdateSession(c *gin.Context) {
	id := getPathID(c)
	entity, err := dops.Get[model.Session](id)
	if err != nil {
		handleNotFound(c, "Session", id)
		return
	}
	var req schema.SessionUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	updates := req.BuildUpdates()
	if len(updates) > 0 {
		dops.Update(entity, updates)
		refreshed, err := dops.Get[model.Session](id)
		if err != nil {
			applogger.Error("failed to refresh session after update", "id", id, "error", err)
		} else {
			entity = refreshed
		}
	}
	sm, err := dops.GetAIPersonInSession(id)
	if err != nil {
		applogger.Error("failed to resolve session person", "session_id", id, "error", err)
	}

	// Resolve current user's participation and unread status.
	isParticipant := false
	hasUnread := false
	if currentUserID, err := dops.GetCurrentUserPersonID(); err != nil {
		applogger.Error("failed to resolve current user person id for participation check", "error", err)
	} else {
		if ok, err := dops.IsParticipant(id, currentUserID); err != nil {
			applogger.Error("failed to resolve user participation in session", "session_id", id, "error", err)
		} else {
			isParticipant = ok
		}
		if isParticipant {
			if us, err := dops.GetUnreadSessions(currentUserID, []int64{id}); err != nil {
				applogger.Error("failed to resolve unread status", "session_id", id, "error", err)
			} else {
				hasUnread = us[id]
			}
		}
	}

	response.Success(c, schema.NewSessionResponse(entity, sm, isParticipant, hasUnread))
}

// DeleteSession handles deleting a session and its resources.
func (h *Handler) DeleteSession(c *gin.Context) {
	id := getPathID(c)

	personID, _, err := dops.DeleteSessionCascade(id)
	if err != nil {
		applogger.Error("DeleteSession: cascade delete failed", "session_id", id, "error", err)
		response.InternalError(c, "Failed to delete session")
		return
	}

	// Filesystem cleanup
	if personID > 0 {
		workspace.RemoveWorkspace(personID, id)
		workspace.RemoveAac(personID, id)
	}
	response.SuccessMessage(c, "Session deleted successfully", nil)
}
