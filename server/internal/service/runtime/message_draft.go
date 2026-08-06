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

// handleDraftCommits processes draft commit requests from the commitCh.
// Runs in a separate goroutine to serialize message writes.
func (r *agentRuntime) handleDraftCommits(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-r.draftCommitCh:
			r.commitDraft(req)
		}
	}
}

// commitDraft atomically commits a draft to the messages table.
// This is the only path through which agent messages enter the messages table.
func (r *agentRuntime) commitDraft(req *draftCommitRequest) {
	if req == nil {
		applogger.Error("commitDraft called with nil commitRequest")
		return
	}

	draft := req.draft
	if draft == nil {
		applogger.Error("commitDraft called with nil draft")
		return
	}

	// tx
	tx := database.DB.Begin()
	defer tx.Rollback()

	// Create the message from the draft content
	msg := model.Message{
		SessionID: draft.SessionID,
		PersonID:  r.agentPersonID,
		Content:   req.content,
		DraftID:   &draft.ID,
	}
	if err := tx.Create(&msg).Error; err != nil {
		applogger.Error("Failed to commit draft to messages",
			"draft_id", draft.ID,
			"session_id", draft.SessionID,
			"error", err,
		)
		return
	}

	// Update draft status and content
	if err := tx.Model(&model.MessageDraft{}).Where("id = ?", draft.ID).Updates(map[string]interface{}{
		"status":  model.DraftStatusCommitted,
		"content": req.content,
	}).Error; err != nil {
		applogger.Error("commitDraft: failed to update draft", "draft_id", draft.ID, "error", err)
		return
	}

	// Update agent's last_active_at and last_read_message_id in the participant session.
	// The agent has "read" everything up to and including its own message,
	// since it produced it based on all prior context.
	if err := tx.Model(&model.ParticipantSession{}).
		Where("session_id = ? AND participant_id = ? AND last_read_message_id < ?",
			draft.SessionID, r.agentPersonID, msg.ID).
		Updates(map[string]interface{}{
			"last_active_at":       time.Now(),
			"last_read_message_id": msg.ID,
		}).Error; err != nil {
		applogger.Error("commitDraft: failed to update participant session", "draft_id", draft.ID, "error", err)
		return
	}

	// Fill empty session title with the first message content.
	// This mirrors the human-AI CreateAndSend path (chat.go), which sets the
	// title from the first message. A2A sessions created via CreateDirectSession
	// start with Title="" — the first committed message fills it here.
	// The WHERE title = '' clause makes this idempotent: once filled, it won't
	// be overwritten by later messages.
	titleRunes := []rune(req.content)
	title := string(titleRunes)
	if len(titleRunes) > 15 {
		title = string(titleRunes[:15]) + "..."
	}
	if err := tx.Model(&model.Session{}).
		Where("id = ? AND title = ?", draft.SessionID, "").
		Update("title", title).Error; err != nil {
		applogger.Error("commitDraft: failed to fill empty session title",
			"session_id", draft.SessionID, "error", err)
		// Non-fatal: the message is already committed; title is cosmetic.
	}

	if err := tx.Commit().Error; err != nil {
		applogger.Error("commitDraft: failed to commit tx", "draft_id", draft.ID, "error", err)
		return
	}

	applogger.Info("Draft committed to messages",
		"draft_id", draft.ID,
		"message_id", msg.ID,
		"session_id", draft.SessionID,
	)

	// Memory: produce event record (sync) + consume self-observation.
	// The agent records its own message as a memory event and creates
	// an observation for itself — "I produced this message".
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

	// Notify other AI participants in the session. This is the agent-to-agent
	// communication path: when an agent commits a message, other agents in the
	// same session receive it as an event in their own eventqueue, processed
	// through their own Comprehend→Decide→Work pipeline. Without this, a
	// message from Agent A to Agent B would sit unread forever.
	r.notifyOtherAIParticipants(draft.SessionID, msg.ID, req.content, eventID)

	// Push message event to SSE clients
	pushMessageEvent(draft.SessionID, msg.ID, msg.PersonID, msg.Content)
}

// notifyOtherAIParticipants sends EventTypeNewPrivateChatMessage events to
// all other AI participants in the session. This is the agent-to-agent
// communication path: when an agent commits a message, other agents in the
// same session receive it as an event in their own eventqueue, processed
// through their own Comprehend→Decide→Work pipeline.
//
// The sending agent's name is resolved from its Person record and used as
// SpeakerName in the event payload. The eventID from the memory system is
// passed along so each receiving agent can create its own observation.
//
// Human participants are NOT notified here — they receive messages via SSE
// (pushMessageEvent). Only AI participants need eventqueue events because
// they have their own cognitive pipelines.
func (r *agentRuntime) notifyOtherAIParticipants(sessionID, messageID int64, content string, eventID int64) {
	aiPersonIDs, err := dops.GetSessionAIParticipantIDs(sessionID)
	if err != nil {
		applogger.Error("notifyOtherAIParticipants: failed to get AI participants",
			"session_id", sessionID, "error", err)
		return
	}

	// Filter out the sender
	var recipientIDs []int64
	for _, id := range aiPersonIDs {
		if id != r.agentPersonID {
			recipientIDs = append(recipientIDs, id)
		}
	}
	if len(recipientIDs) == 0 {
		return
	}

	// Resolve sender's name for the SpeakerName field
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
