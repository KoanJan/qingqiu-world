package runtime

import (
	"context"
	"time"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
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

	if err := tx.Commit().Error; err != nil {
		applogger.Error("commitDraft: failed to commit tx", "draft_id", draft.ID, "error", err)
		return
	}

	applogger.Info("Draft committed to messages",
		"draft_id", draft.ID,
		"message_id", msg.ID,
		"session_id", draft.SessionID,
	)

	// Submit to the event vectorization service for embedding + observation.
	memory.SubmitVectorization(memory.VectorizationTask{
		MessageID: msg.ID,
		SessionID: msg.SessionID,
		Content:   msg.Content,
	})

	// Push message event to SSE clients
	pushMessageEvent(draft.SessionID, msg.ID, msg.Content)
}
