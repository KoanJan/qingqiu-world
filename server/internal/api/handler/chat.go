// Package handler implements the HTTP API handlers for the chat system.
//
// This package provides the Gin-based HTTP handlers that expose the chat
// functionality via REST API endpoints. It handles:
//   - Creating new sessions and sending the first message
//   - Sending messages to existing sessions
//   - Streaming AI responses via Server-Sent Events (SSE)
//   - Managing SSE connection lifecycle
//   - Triggering background summary generation
//
// The handler layer is responsible for:
//   - Request validation and parameter extraction
//   - Database record creation (session, messages)
//   - Asynchronous chat processing via goroutines
//   - SSE event broadcasting to connected clients
//   - Error handling and graceful degradation
package handler

import (
	"strconv"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/realtime/sse"
	"qingqiu-world-server/internal/service/runtime"

	applogger "qingqiu-world-server/internal/logger"

	"qingqiu-world-server/internal/api/response"

	"github.com/gin-gonic/gin"
)

// userFriendlyErrorMessage is the default error message shown to users on internal errors.
const userFriendlyErrorMessage = "Sorry, something went wrong on the server. Please try again later."

// CreateAndSend creates a new session and sends the first message.
//
// This is the entry point for new conversations. It:
//  1. Creates a new session with the message as title
//  2. Creates the user message record
//  3. Triggers summary generation if needed
//  4. Sends an event to the Agent Runtime (no placeholder AI message)
//
// The Agent Runtime will create a Work, which uses a draft for content
// accumulation. When the Work completes, the draft is committed to the
// messages table and pushed via SSE.
//
// Returns session_id and message_id.
func (h *Handler) CreateAndSend(c *gin.Context) {
	message := c.Query("message")
	if message == "" {
		response.BadRequest(c, "message is required")
		return
	}

	agentIDStr := c.Query("agent_id")
	var agentPersonID int64
	var agentConfigID int64
	if agentIDStr != "" {
		agentPersonID, _ = strconv.ParseInt(agentIDStr, 10, 64)
	}
	applogger.Info("CreateAndSend received agent_id param", "raw", agentIDStr, "parsed", agentPersonID)
	if agentPersonID == 0 {
		var defaultAgentConfig model.AgentConfig
		if err := database.DB.First(&defaultAgentConfig).Error; err != nil {
			response.InternalError(c, "No default agent found")
			return
		}
		agentPersonID = defaultAgentConfig.PersonID
		agentConfigID = defaultAgentConfig.ID
	} else {
		// Resolve agent config by person ID for event routing
		var ac model.AgentConfig
		if err := database.DB.Where("person_id = ?", agentPersonID).First(&ac).Error; err != nil {
			response.InternalError(c, "No agent config found for person")
			return
		}
		agentConfigID = ac.ID
	}

	userPersonID, err := dops.GetCurrentUserPersonID()
	if err != nil {
		response.BadRequest(c, "No user profile found. Please set up your profile in Settings first.")
		return
	}

	title := c.Query("title")
	if title == "" {
		runes := []rune(message)
		if len(runes) > 15 {
			title = string(runes[:15]) + "..."
		} else {
			title = message
		}
	}

	session := model.Session{
		Title: title,
	}
	userMsg := model.Message{
		SessionID: session.ID,
		PersonID:  userPersonID,
		Content:   message,
	}

	err = dops.CreateSession(&session, &userMsg, userPersonID, agentPersonID)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}

	// Produce memory event + dispatch to agent runtime
	runtime.SendNewMessageEvent(agentConfigID, session.ID, userMsg.ID, userPersonID, message, dops.GetUserName())

	response.Success(c, gin.H{
		"session_id": session.ID,
		"message_id": userMsg.ID,
	})
}

// SendMessage sends a message to an existing session.
//
// This is the entry point for continuing conversations. It:
//  1. Validates the session exists
//  2. Creates the user message record
//  3. Triggers summary generation if needed
//  4. Sends an event to the Agent Runtime (no placeholder AI message)
//
// The Agent Runtime handles the event asynchronously — if an active Work
// exists in this session, the event is absorbed; otherwise a new Work is created.
//
// Returns message_id.
func (h *Handler) SendMessage(c *gin.Context) {
	sessionID := getPathIDByParam(c, "session_id")

	var session model.Session
	if err := database.DB.First(&session, sessionID).Error; err != nil {
		response.NotFound(c, "Session not found")
		return
	}

	message := c.Query("message")
	if message == "" {
		response.BadRequest(c, "message is required")
		return
	}

	userPersonID, err := dops.GetCurrentUserPersonID()
	if err != nil {
		response.BadRequest(c, "No user profile found. Please set up your profile in Settings first.")
		return
	}

	userMsg := model.Message{
		SessionID: sessionID,
		PersonID:  userPersonID,
		Content:   message,
	}
	if err := dops.CreateMessage(&userMsg); err != nil {
		response.InternalError(c, err.Error())
		return
	}

	// Update user's last_read_message_id — user has seen all messages up to this point
	if err := dops.UpdateLastReadMessageID(sessionID, userPersonID, userMsg.ID); err != nil {
		applogger.Error("failed to update last_read_message_id on continue", "session_id", sessionID, "error", err)
	}

	// Produce memory event + dispatch to agent runtime
	agentConfigID := dops.GetFirstAgentConfigIDBySessionID(sessionID)
	runtime.SendNewMessageEvent(agentConfigID, sessionID, userMsg.ID, userPersonID, message, dops.GetUserName())

	response.Success(c, gin.H{
		"message_id": userMsg.ID,
	})
}

// StreamNotifications serves the single, user-scoped event stream. The current
// human is resolved before subscribing, so a connection can receive only that
// user's fan-out set. Session routing is represented in event.session_id, not
// in the URL; switching chats therefore does not create another connection.
func (h *Handler) StreamNotifications(c *gin.Context) {
	humanPersonID, err := dops.GetCurrentUserPersonID()
	if err != nil {
		response.BadRequest(c, "No user profile found.")
		return
	}

	if h.hub == nil {
		response.InternalError(c, "Realtime hub unavailable")
		return
	}
	sse.Serve(c.Writer, c.Request, h.hub.Subscribe(humanPersonID))
}

// sessionAgentStatus represents an agent's status within a session.
type sessionAgentStatus struct {
	AgentID int64  `json:"agent_id"`
	Name    string `json:"name"`
	Avatar  string `json:"avatar"`
	Status  int    `json:"status"` // 0=idle, 1=working
}

// GetSessionAgents returns all agents in a session with their current status.
// Used by the frontend to display agent status indicators.
func (h *Handler) GetSessionAgents(c *gin.Context) {
	sessionIDStr := c.Param("session_id")
	sessionID, err := strconv.ParseInt(sessionIDStr, 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid session_id")
		return
	}

	// Find all AI participants in this session by joining persons table (type=1)
	participants, err := dops.ListAIParticipants(sessionID)
	if err != nil {
		response.InternalError(c, "failed to query participants")
		return
	}

	// todo avatart should be in person table
	result := make([]sessionAgentStatus, 0, len(participants))
	for _, p := range participants {
		person, err := dops.GetPerson(p.ParticipantID)
		if err != nil {
			applogger.Error("failed to find agent by person ID for session participants", "person_id", p.ParticipantID, "error", err)
			continue
		}

		result = append(result, sessionAgentStatus{
			AgentID: p.ParticipantID,
			Name:    person.Name,
			Avatar:  person.Avatar,
			Status:  p.Status, // Read directly from ParticipantSession.Status
		})
	}

	response.Success(c, result)
}
