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
	"encoding/json"
	"io"
	"strconv"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/runtime"

	applogger "qingqiu-world-server/internal/logger"

	"qingqiu-world-server/internal/api/response"

	"github.com/gin-gonic/gin"
)

// userFriendlyErrorMessage is the default error message shown to users on internal errors.
const userFriendlyErrorMessage = "Sorry, something went wrong on the server. Please try again later."

// connectionManager manages SSE connections per session.
// Each session can have multiple connected clients (e.g., multiple browser tabs).
// Messages are broadcast to all connections of the same session.
type connectionManager struct {
	connections map[int64][]chan string // sessionID -> list of SSE channels
}

// connManager is the global singleton for managing SSE connections.
var connManager = &connectionManager{
	connections: make(map[int64][]chan string),
}

// Register creates and registers a new SSE channel for a session.
// Returns the channel for the caller to listen on.
func (cm *connectionManager) Register(sessionID int64) chan string {
	ch := make(chan string, 256)
	cm.connections[sessionID] = append(cm.connections[sessionID], ch)
	return ch
}

// Unregister removes an SSE channel from a session and closes it.
// Cleans up the session entry if no connections remain.
func (cm *connectionManager) Unregister(sessionID int64, ch chan string) {
	conns := cm.connections[sessionID]
	for i, c := range conns {
		if c == ch {
			cm.connections[sessionID] = append(conns[:i], conns[i+1:]...)
			close(c)
			break
		}
	}
	if len(cm.connections[sessionID]) == 0 {
		delete(cm.connections, sessionID)
	}
}

// PushToSession sends a message to all SSE channels of a session.
// Drops the message if a channel is full (non-blocking send).
func (cm *connectionManager) PushToSession(sessionID int64, data string) {
	conns := cm.connections[sessionID]
	for _, ch := range conns {
		select {
		case ch <- data:
		default:
			// TODO: Notify the client to refresh and reset the SSE connection when messages
			// are dropped. Without this, the client will have an incomplete message list,
			// causing a cognitive gap for the user who sees stale/partial data.
			applogger.Error("SSE channel full, dropping message", "session_id", sessionID)
		}
	}
}

// CloseAll closes all SSE channels and clears the connection map.
// Causes all StreamMessages handlers to exit cleanly.
func (cm *connectionManager) CloseAll() {
	for sessionID, conns := range cm.connections {
		for _, ch := range conns {
			close(ch)
		}
		delete(cm.connections, sessionID)
	}
}

// PushSSEToSession is the exported wrapper for pushing SSE events to a session.
// Used by the runtime package to push agent_status and message events.
func PushSSEToSession(sessionID int64, data string) {
	connManager.PushToSession(sessionID, data)
}

// ShutdownSSE closes all SSE connections gracefully.
// Should be called before HTTP server shutdown to avoid
// waiting for long-lived keep-alive connections to close.
func ShutdownSSE() {
	connManager.CloseAll()
}

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
	runtime.SendNewMessageEvent(agentConfigID, session.ID, userMsg.ID, message, dops.GetUserName())

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
	runtime.SendNewMessageEvent(agentConfigID, sessionID, userMsg.ID, message, dops.GetUserName())

	response.Success(c, gin.H{
		"message_id": userMsg.ID,
	})
}

// StreamMessages handles SSE streaming for a session.
//
// Establishes a Server-Sent Events connection that:
//  1. Sends any existing streaming message content (reconnection support)
//  2. Registers an SSE channel for real-time updates
//  3. Streams chunks, notifications, and done/error events
//  4. Sends heartbeat keep-alive every 30 seconds
//  5. Cleans up on client disconnect or stream completion
func (h *Handler) StreamMessages(c *gin.Context) {
	sessionID := getPathIDByParam(c, "session_id")

	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	_, err := dops.GetSession(sessionID)
	if err != nil {
		errorData, _ := json.Marshal(map[string]string{"type": "error", "message": "Session not found"})
		c.SSEvent("", string(errorData))
		return
	}

	ch := connManager.Register(sessionID)
	defer connManager.Unregister(sessionID, ch)

	c.Stream(func(w io.Writer) bool {
		heartbeat := time.NewTimer(30 * time.Second)
		defer heartbeat.Stop()

		select {
		case data, ok := <-ch:
			if !ok {
				return false
			}
			c.SSEvent("", data)
			var parsed map[string]interface{}
			if json.Unmarshal([]byte(data), &parsed) == nil {
				if t, ok := parsed["type"].(string); ok && (t == "done" || t == "error") {
					return false
				}
			}
			return true
		case <-c.Request.Context().Done():
			return false
		case <-heartbeat.C:
			c.Writer.WriteString(": heartbeat\n\n")
			c.Writer.Flush()
			return true
		}
	})
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
