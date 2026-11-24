package runtime

import (
	"context"
	"time"

	"gorm.io/gorm"

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
	// Sending is an action, not comprehension of preceding incoming messages.
	// The read boundary advances only with a persisted chat Decision.
	// Updating the participant row also verifies membership at the final write
	// boundary. A wrong session ID must not create a Message or its causal facts.
	member := tx.Model(&model.ParticipantSession{}).
		Where("session_id = ? AND participant_id = ?", req.sessionID, r.agentPersonID).
		Update("last_active_at", time.Now())
	if member.Error != nil || member.RowsAffected != 1 {
		applogger.Error("commitMessage: sender is not a unique session participant",
			"session_id", req.sessionID, "person_id", r.agentPersonID,
			"rows_affected", member.RowsAffected, "error", member.Error)
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

	recipients, err := messageRecipientsTx(tx, req.sessionID, r.agentPersonID)
	if err != nil {
		applogger.Error("commitMessage: resolve recipients", "session_id", req.sessionID, "error", err)
		return
	}
	var sender model.Person
	if err := tx.First(&sender, r.agentPersonID).Error; err != nil {
		applogger.Error("commitMessage: load sender", "person_id", r.agentPersonID, "error", err)
		return
	}
	if err := eventqueue.AppendTx(tx, messageDelivery(req.sessionID, msg.ID, eventID, msg.Content, sender.Name), recipients...); err != nil {
		applogger.Error("commitMessage: persist recipient deliveries", "event_id", eventID, "error", err)
		return
	}

	if err := tx.Commit().Error; err != nil {
		applogger.Error("commitMessage: failed to commit tx",
			"session_id", req.sessionID, "error", err)
		return
	}
	committed = true
	// A sent message is also a new self-observation. Stop background cognition
	// so its in-flight model response cannot be accepted as if it saw this fact.
	r.pauseCurrentCognition()
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
	eventqueue.Notify(recipients...)

	// Push message event to SSE clients.
	notify(notification.MessageCommitted{SessionID: req.sessionID, MessageID: msg.ID, PersonID: msg.PersonID, Content: msg.Content, ExpressionInstruction: msg.ExpressionInstruction})
}

// messageRecipientsTx resolves the intended active AI recipients in the same
// transaction as the Event, excluding the sender.
func messageRecipientsTx(tx *gorm.DB, sessionID, senderID int64) ([]int64, error) {
	var ids []int64
	err := tx.Model(&model.ParticipantSession{}).
		Where("session_id = ? AND participant_id != ? AND participant_id IN (SELECT id FROM persons WHERE type = ? AND status = ?)", sessionID, senderID, model.PersonTypeAI, model.PersonStatusActive).
		Order("participant_id").Pluck("participant_id", &ids).Error
	return ids, err
}

func messageDelivery(sessionID, messageID, eventID int64, content, speakerName string) *eventqueue.AgentEvent {
	return &eventqueue.AgentEvent{Type: eventqueue.EventTypeNewPrivateChatMessage, SessionID: sessionID, EventID: eventID,
		Payload: &eventqueue.NewMessagePayload{MessageID: messageID, MessageContent: content, SpeakerName: speakerName}}
}
