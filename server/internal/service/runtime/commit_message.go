package runtime

import (
	"context"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/memory"
)

// handleMessageCommits processes message commit requests from commitCh.
// Runs in a separate goroutine to serialize message writes.
func (r *agentRuntime) handleMessageCommits(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-r.messageCommitCh:
			r.commitMessage(req)
		}
	}
}

// commitRequest carries the data needed to commit a message.
type commitRequest struct {
	sessionID int64
	content   string
}

// commitMessage atomically creates a message record and performs all
// post-commit side effects: participant session update, session title
// fill, memory event recording, A2A notification, and SSE push.
func (r *agentRuntime) commitMessage(req *commitRequest) {
	if req == nil {
		applogger.Error("commitMessage called with nil commitRequest")
		return
	}

	tx := database.DB.Begin()
	defer tx.Rollback()

	msg := &model.Message{
		SessionID: req.sessionID,
		PersonID:  r.agentPersonID,
		Content:   req.content,
	}
	if err := tx.Create(msg).Error; err != nil {
		applogger.Error("commitMessage: failed to create message",
			"session_id", req.sessionID,
			"error", err,
		)
		return
	}

	// Update agent's last_active_at and last_read_message_id.
	if err := tx.Model(&model.ParticipantSession{}).
		Where("session_id = ? AND participant_id = ? AND last_read_message_id < ?",
			req.sessionID, r.agentPersonID, msg.ID).
		Updates(map[string]interface{}{
			"last_active_at":       time.Now(),
			"last_read_message_id": msg.ID,
		}).Error; err != nil {
		applogger.Error("commitMessage: failed to update participant session",
			"session_id", req.sessionID, "error", err)
		return
	}

	// Fill empty session title with the first message content.
	titleRunes := []rune(req.content)
	title := string(titleRunes)
	if len(titleRunes) > 15 {
		title = string(titleRunes[:15]) + "..."
	}
	if err := tx.Model(&model.Session{}).
		Where("id = ? AND title = ?", req.sessionID, "").
		Update("title", title).Error; err != nil {
		applogger.Error("commitMessage: failed to fill empty session title",
			"session_id", req.sessionID, "error", err)
		// Non-fatal: the message is already committed; title is cosmetic.
	}

	if err := tx.Commit().Error; err != nil {
		applogger.Error("commitMessage: failed to commit tx",
			"session_id", req.sessionID, "error", err)
		return
	}

	applogger.Info("Message committed",
		"message_id", msg.ID,
		"session_id", req.sessionID,
	)

	// Memory: produce event record (sync) + consume self-observation.
	eventID, err := memory.RecordEvent(msg.ID, msg.Content)
	if err != nil {
		applogger.Error("failed to record memory event for agent message",
			"message_id", msg.ID, "error", err)
	} else {
		if err := memory.CreateObservation(r.agentPersonID, eventID); err != nil {
			applogger.Error("failed to create self-observation",
				"person_id", r.agentPersonID, "event_id", eventID, "error", err)
		}
	}

	// Notify other AI participants in the session.
	r.notifyOtherAIParticipants(req.sessionID, msg.ID, req.content, eventID)

	// Push message event to SSE clients.
	pushMessageEvent(req.sessionID, msg.ID, msg.PersonID, msg.Content)
}

// notifyOtherAIParticipants sends EventTypeNewPrivateChatMessage events to
// all other AI participants in the session. This is the agent-to-agent
// communication path: when an agent commits a message, other agents in the
// same session receive it as an event in their own eventqueue.
//
// The sending agent's name is resolved from its Person record and used as
// SpeakerName in the event payload. The eventID from the memory system is
// passed along so each receiving agent can create its own observation.
func (r *agentRuntime) notifyOtherAIParticipants(sessionID, messageID int64, content string, eventID int64) {
	aiPersonIDs, err := dops.GetSessionAIParticipantIDs(sessionID)
	if err != nil {
		applogger.Error("notifyOtherAIParticipants: failed to get AI participants",
			"session_id", sessionID, "error", err)
		return
	}

	var recipientIDs []int64
	for _, id := range aiPersonIDs {
		if id != r.agentPersonID {
			recipientIDs = append(recipientIDs, id)
		}
	}
	if len(recipientIDs) == 0 {
		return
	}

	sender, err := dops.GetPerson(r.agentPersonID)
	if err != nil {
		applogger.Error("notifyOtherAIParticipants: failed to get sender name",
			"person_id", r.agentPersonID, "error", err)
		return
	}

	for _, recipientPersonID := range recipientIDs {
		ac, err := dops.GetAgentConfigByPersonID(recipientPersonID)
		if err != nil {
			applogger.Error("notifyOtherAIParticipants: failed to resolve agent config",
				"person_id", recipientPersonID, "error", err)
			continue
		}
		eventqueue.SendEvent(ac.ID, &eventqueue.AgentEvent{
			Type:      eventqueue.EventTypeNewPrivateChatMessage,
			SessionID: sessionID,
			EventID:   eventID,
			Payload: &eventqueue.NewMessagePayload{
				MessageID:      messageID,
				MessageContent: content,
				SpeakerName:    sender.Name,
			},
		})
		applogger.Info("Notified AI participant of new message",
			"session_id", sessionID,
			"message_id", messageID,
			"sender_person_id", r.agentPersonID,
			"recipient_person_id", recipientPersonID,
			"recipient_agent_config_id", ac.ID,
		)
	}
}
