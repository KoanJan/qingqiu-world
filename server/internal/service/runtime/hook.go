package runtime

import applogger "qingqiu-world-server/internal/logger"

// ==========================================================================
// Integration Hooks
// ==========================================================================

// pushMessageEvent pushes a message event to SSE clients.
// personID identifies who sent the message — needed by the frontend to
// render the correct sender avatar, especially in A2A sessions where
// both participants are agents.
// This is a package-level function that will be connected to the
// handler's ConnectionManager during integration.
var pushMessageEvent = func(sessionID, messageID, personID int64, content string) {
	// Default no-op; will be overridden during integration
	applogger.Debug("pushMessageEvent called (not integrated)",
		"session_id", sessionID,
		"message_id", messageID,
		"person_id", personID,
	)
}

// pushSSEEvent pushes a raw SSE event to all clients of a session.
// Used for notifications and other non-message events.
var pushSSEEvent = func(sessionID int64, data string) {
	// Default no-op; will be overridden during integration
	applogger.Debug("pushSSEEvent called (not integrated)",
		"session_id", sessionID,
	)
}
