package runtime

import (
	"context"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/notification"
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
	actionID              int64
	sessionID             int64
	content               string
	expressionInstruction string
}

// commitMessage atomically creates the message, Event, Action effect and
// sender observation, then publishes derived data and notifications.
func (r *agentRuntime) commitMessage(req *commitRequest) {
	if req == nil {
		applogger.Error("commitMessage called with nil commitRequest")
		return
	}
	if req.actionID <= 0 {
		applogger.Error("commitMessage called without persisted Action ID", "session_id", req.sessionID)
		return
	}
	committed := false
	defer func() {
		if !committed {
			if err := endAction(req.actionID); err != nil {
				applogger.Error("commitMessage: failed to end unsuccessful action", "action_id", req.actionID, "error", err)
			}
		}
	}()

	tx := database.DB.Begin()
	defer tx.Rollback()
	if err := dops.RequireActivePersonTx(tx, r.agentPersonID); err != nil {
		applogger.Error("commitMessage: sender unavailable", "person_id", r.agentPersonID, "error", err)
		return
	}
	if err := dops.RequireActiveSessionTx(tx, req.sessionID); err != nil {
		applogger.Error("commitMessage: session unavailable", "session_id", req.sessionID, "error", err)
		return
	}

	msg := &model.Message{
		SessionID:             req.sessionID,
		PersonID:              r.agentPersonID,
		Content:               req.content,
		ExpressionInstruction: req.expressionInstruction,
	}
	if err := tx.Create(msg).Error; err != nil {
		applogger.Error("commitMessage: failed to create message",
			"session_id", req.sessionID,
			"error", err,
		)
		return
	}
	eventID, err := memory.RecordReferencedEventTx(tx, model.EventTypeMessage, msg.ID)
	if err != nil {
		applogger.Error("commitMessage: failed to create message Event", "action_id", req.actionID, "error", err)
		return
	}
	if err := recordActionEffect(tx, req.actionID, model.ActionEffectMessage, msg.ID); err != nil {
		applogger.Error("commitMessage: failed to record message source", "action_id", req.actionID, "error", err)
		return
	}
	if err := tx.Model(&model.Action{}).Where("id = ?", req.actionID).
		Update("status", model.ActionStatusEnded).Error; err != nil {
		applogger.Error("commitMessage: failed to finish action", "action_id", req.actionID, "error", err)
		return
	}

	// Sending is an action, not comprehension of preceding incoming messages.
	// The read boundary advances only with a persisted chat Decision.
	if err := tx.Model(&model.ParticipantSession{}).
		Where("session_id = ? AND participant_id = ?", req.sessionID, r.agentPersonID).
		Update("last_active_at", time.Now()).Error; err != nil {
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
	obsID, err := memory.CreateObservationTx(tx, r.agentPersonID, eventID)
	if err != nil {
		applogger.Error("commitMessage: failed to create self-observation", "person_id", r.agentPersonID, "event_id", eventID, "error", err)
		return
	}

	if err := tx.Commit().Error; err != nil {
		applogger.Error("commitMessage: failed to commit tx",
			"session_id", req.sessionID, "error", err)
		return
	}
	committed = true
	if obsID > 0 {
		applogger.Debug("Observation created", "person_id", r.agentPersonID, "event_id", eventID, "obs_id", obsID)
	}
	refreshMemorySource(model.MemorySourceEvent, eventID)
	refreshMemorySource(model.MemorySourceAction, req.actionID)

	applogger.Info("Message committed",
		"message_id", msg.ID,
		"session_id", req.sessionID,
	)

	// Vectorization follows the committed Message/Event fact.
	memory.EnqueueEventEmbedding(eventID, msg.Content)

	// Notify other AI participants in the session.
	r.notifyOtherAIParticipants(req.sessionID, msg.ID, req.content, eventID)

	// Push message event to SSE clients.
	notify(notification.MessageCommitted{SessionID: req.sessionID, MessageID: msg.ID, PersonID: msg.PersonID, Content: msg.Content, ExpressionInstruction: msg.ExpressionInstruction})
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
