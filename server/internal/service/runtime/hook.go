package runtime

import applogger "qingqiu-world-server/internal/logger"

// ==========================================================================
// Integration Hooks
// ==========================================================================

// pushMessageEvent pushes a message event to SSE clients.
// This is a package-level function that will be connected to the
// handler's ConnectionManager during integration.
var pushMessageEvent = func(sessionID, messageID int64, content string) {
	// Default no-op; will be overridden during integration
	applogger.Debug("pushMessageEvent called (not integrated)",
		"session_id", sessionID,
		"message_id", messageID,
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
