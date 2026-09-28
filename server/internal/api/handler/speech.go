package handler

import (
	"context"
	"errors"
	"os"
	"time"

	"gorm.io/gorm"

	"qingqiu-world-server/internal/api/response"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/speech"

	"github.com/gin-gonic/gin"
)

const speechWaitWindow = 20 * time.Second

// GetMessageSpeech returns immutable audio or ensures one on-demand render.
func (h *Handler) GetMessageSpeech(c *gin.Context) {
	// Rendering is user-triggered even though this is a GET endpoint. Never
	// allow a browser or intermediary to cache either an audio result or error.
	c.Header("Cache-Control", "no-store")
	messageID := getPathID(c)
	if !canAccessMessageSpeech(messageID) {
		response.NotFound(c, "Message not found")
		return
	}
	history, err := speech.EnsureRender(messageID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.NotFound(c, "Message not found")
			return
		}
		applogger.Error("ensure speech render failed", "message_id", messageID, "error", err)
		response.BadRequest(c, "Speech is unavailable. Please try again later.")
		return
	}
	if history.Status != model.SpeechRenderStatusSuccess {
		waitCtx, cancel := context.WithTimeout(c.Request.Context(), speechWaitWindow)
		history, err = speech.WaitForRender(waitCtx, messageID)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			applogger.Error("wait for speech render failed", "message_id", messageID, "error", err)
		}
	}
	if history == nil || history.Status != model.SpeechRenderStatusSuccess {
		// The client gave up waiting. Record why so a stalled or failing worker
		// is never silently reported as a generic "not ready".
		fields := []any{"message_id", messageID, "wait_window_ms", speechWaitWindow.Milliseconds()}
		if history != nil {
			fields = append(fields, "history_id", history.ID, "status", history.Status, "error_message", history.ErrorMessage)
		}
		applogger.Warn("speech render not ready within wait window", fields...)
		response.InternalError(c, "Speech is not ready. Please try again later.")
		return
	}
	path, err := speech.ResolveStoredAudio(history.RelativePath)
	if err != nil {
		applogger.Error("resolve speech audio path failed", "message_id", messageID, "history_id", history.ID, "error", err)
		response.InternalError(c, "Speech is unavailable. Please try again later.")
		return
	}
	if _, err := os.Stat(path); err != nil {
		applogger.Error("speech history output is missing", "message_id", messageID, "history_id", history.ID, "error", err)
		response.InternalError(c, "Speech is unavailable. Please try again later.")
		return
	}
	c.Header("Content-Type", history.MIMEType)
	c.File(path)
}

// canAccessMessageSpeech reports whether the current user may listen to one
// message. A non-NotFound lookup failure is logged so an authorization or
// storage fault is never silently reported to the client as "not found".
func canAccessMessageSpeech(messageID int64) bool {
	message, err := dops.Get[model.Message](messageID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			applogger.Error("speech access check: load message failed", "message_id", messageID, "error", err)
		}
		return false
	}
	person, err := dops.GetPerson(message.PersonID)
	if err != nil {
		applogger.Error("speech access check: load person failed", "message_id", messageID, "person_id", message.PersonID, "error", err)
		return false
	}
	if person.Type != model.PersonTypeAI {
		return false
	}
	currentUserID, err := dops.GetCurrentUserPersonID()
	if err != nil {
		applogger.Error("speech access check: load current user failed", "message_id", messageID, "error", err)
		return false
	}
	sessionUserID, err := dops.GetSessionHumanParticipantID(message.SessionID)
	if err != nil {
		applogger.Error("speech access check: load session participant failed", "message_id", messageID, "session_id", message.SessionID, "error", err)
		return false
	}
	return currentUserID == sessionUserID
}
