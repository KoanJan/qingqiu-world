package runtime

import (
	"context"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/chat"
	"qingqiu-world-server/internal/service/comprehend"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/task"
)

// userFriendlyErrorMsg is the default error message shown to users when
// server-side processing fails.
const userFriendlyErrorMsg = "Sorry, something went wrong on the server. Please try again later."

// work represents a unit of work for an agent within a session.
// It is created when an agent decides to act on an event, and it may
// absorb subsequent events (e.g., user corrections) during its execution.
//
// Three-layer model: Agent (long-lived) → work (coherent goal) → Iteration (atomic ReAct step)
//
// work unifies the Chat path (single LLM call) and Task path (ReAct loop):
//   - Chat work: single LLM call, absorbs events before context assembly
//   - Task work: ReAct loop, absorbs events at each iteration boundary
//
// After the cognitive order refactoring, work holds a WorkPlan (carrying the
// Decide phase's execution intent as Guidance) and comprehension results
// (carrying the Comprehend phase's understanding). This ensures the execution
// layer has full context without re-interpreting the event.
type work struct {
	ID             int64
	agent          *agentRuntime
	sessionID      int64
	draft          *model.MessageDraft
	plan           *WorkPlan // From Decide phase: type + guidance
	maxIterations  int
	initialPayload any                             // The payload from the event that created this work
	comprehension  *comprehend.ComprehensionResult // Results from the Comprehend phase
	taskResult     *task.TaskResult                // Task execution result (only set for TaskWork)
	guidanceCh     chan task.GuidanceDirective     // Channel for sending guidance/cancel directives to TaskLoop
	done           chan struct{}                   // Closed when work finishes (normal or abandoned)

	// startedAt is set when work begins running, read by buildActiveWorksContext
	// so the Decide LLM can see how long a work has been running.
	startedAt time.Time // Set in Run(), read by Decide
}

// Run executes the work. After the cognitive order refactoring, the execution
// path is determined by plan.Type:
//   - WorkTypeTask: RunTask (execute with Guidance) only. No reply generation.
//     On completion, signals the event loop, which creates a ChatWork to inform the user.
//   - WorkTypeChat: ExecuteChat (context assembly + LLM response).
//     This is the only path that generates a reply.
//
// Both paths receive comprehension results from the Comprehend phase,
// skipping redundant preprocessing, person state inference, and KB retrieval.
// On completion, commits the draft and signals the event loop to remove this work.
// Respects context cancellation: exits early if the work is cancelled.
func (w *work) Run(ctx context.Context) {
	w.startedAt = time.Now() // Record start time for Decide-phase duration awareness
	defer close(w.done)      // Signal completion regardless of how work exits

	defer func() {
		// Only transition to Completed if still Running.
		// If abandon() already set Abandoned, this update is a no-op.
		if err := database.DB.Model(&model.Work{}).
			Where("id = ? AND status = ?", w.ID, model.WorkStatusRunning).
			Update("status", model.WorkStatusCompleted).Error; err != nil {
			applogger.Error("work: failed to update work status", "work_id", w.ID, "error", err)
		}

		// Send work completed event to the agent's event queue.
		// The agent processes this through the same Comprehend→Decide pipeline
		// as external events, deciding whether to inform the user.
		// This replaces the old workDoneCh + auto-create-ChatWork pattern.
		status := "success"
		var output, taskErr string
		if w.taskResult != nil {
			if w.taskResult.Status != "success" {
				status = "failure"
				taskErr = w.taskResult.Error
			} else {
				output = w.taskResult.Output
			}
		}

		eventqueue.SendEvent(w.agent.agentConfigID, &eventqueue.AgentEvent{
			Type:      eventqueue.EventTypeWorkCompleted,
			SessionID: w.sessionID,
			Payload: &eventqueue.WorkCompletedPayload{
				WorkID:     w.ID,
				WorkType:   int(w.plan.Type),
				Guidance:   w.plan.Guidance,
				Status:     status,
				TaskOutput: output,
				TaskError:  taskErr,
				Trigger:    w.getTrigger(),
			},
		})
	}()

	applogger.Info("work started",
		"work_id", w.ID,
		"session_id", w.sessionID,
		"type", w.plan.Type,
		"guidance", w.plan.Guidance,
	)

	// Check cancellation before starting
	if ctx.Err() != nil {
		applogger.Info("work cancelled before pipeline", "work_id", w.ID)
		w.abandon()
		return
	}

	switch w.plan.Type {
	case model.WorkTypeTask:
		w.runTask(ctx)
	case model.WorkTypeChat:
		w.runChat(ctx)
	default:
		applogger.Error("unknown type of work plan", "work_type", w.plan.Type)
	}

	applogger.Info("work completed",
		"work_id", w.ID,
		"session_id", w.sessionID,
	)
}

// runTask executes the task path using Guidance from the Decide phase.
// TaskWork is pure execution — it does NOT generate a reply.
// The event loop's workDoneCh handler will create a ChatWork to inform the user.
func (w *work) runTask(ctx context.Context) {
	session, ac, llmConfig := w.loadChatDependencies()
	if session == nil || ac == nil || llmConfig == nil {
		w.abandon()
		return
	}

	w.taskResult = task.RunTask(task.RunTaskParams{
		LLMConfig:  llmConfig,
		SessionID:  w.sessionID,
		PersonID:   ac.PersonID,
		WorkID:     w.ID,
		Guidance:   w.plan.Guidance,
		Background: w.plan.Background,
		Metadata:   w.plan.Metadata,
		Ctx:        ctx,
		OnNotify:   func(data string) { pushSSEEvent(w.sessionID, data) },
		GuidanceCh: w.guidanceCh,
	})

	applogger.Info("TaskWork completed",
		"work_id", w.ID,
		"session_id", w.sessionID,
		"status", w.taskResult.Status,
	)

	// TaskWork does not generate a reply, so discard the draft.
	// The ChatWork created by the event loop will have its own draft.
	w.discardDraft()
}

// discardDraft marks the draft as discarded. Used by TaskWork since it
// does not produce a reply — the draft was only needed for the audit trail.
func (w *work) discardDraft() {
	if w.draft != nil {
		if err := database.DB.Model(&model.MessageDraft{}).Where("id = ?", w.draft.ID).
			Update("status", model.DraftStatusDiscarded).Error; err != nil {
			applogger.Error("work: failed to discard draft", "draft_id", w.draft.ID, "error", err)
		}
	}
}

// FeedGuidance sends a guidance directive to the work's guidance channel.
// This is called when the Decide phase routes an event to an existing
// TaskWork or cancels it — the directive becomes an environment event
// that the TaskLoop observes at the next iteration boundary.
//
// For cancel, the directive carries guidance like "save progress and stop"
// and the reason explaining why. The TaskLoop's LLM processes this and
// decides how to wrap up — this is "appealable" cancellation, not forceful kill.
func (w *work) FeedGuidance(directive task.GuidanceDirective) {
	if w.guidanceCh == nil {
		applogger.Error("FeedGuidance called on work with nil guidanceCh",
			"work_id", w.ID,
		)
		return
	}
	select {
	case w.guidanceCh <- directive:
		applogger.Info("Guidance fed to work",
			"work_id", w.ID,
			"guidance", directive.Guidance,
			"reason", directive.Reason,
		)
	default:
		applogger.Error("work guidanceCh full, dropping guidance",
			"work_id", w.ID,
			"guidance", directive.Guidance,
		)
	}
}

// runChat executes the chat path: context assembly + LLM response.
// This is the only path that generates a reply to the user.
func (w *work) runChat(ctx context.Context) {
	session, ac, llmConfig := w.loadChatDependencies()
	if session == nil || ac == nil || llmConfig == nil {
		w.handleChatError()
		return
	}

	draftID := int64(0)
	if w.draft != nil {
		draftID = w.draft.ID
	}

	var triggerOverride *chat.TriggerOverride
	if payload, ok := w.initialPayload.(*eventqueue.ScheduledEventPayload); ok {
		triggerOverride = &chat.TriggerOverride{
			Type:    chat.TriggerOverrideScheduledAlarm,
			Content: payload.Message,
		}
	}

	// Convert ComprehensionResult to ComprehensionInput for the chat package.
	var comprehensionInput *chat.ComprehensionInput
	if w.comprehension != nil {
		comprehensionInput = &chat.ComprehensionInput{
			PersonState:        w.comprehension.PersonState,
			HistorySegments:    historySegments(w.comprehension.HistorySearch),
			KBSegments:         kbSegments(w.comprehension.KBRetrieval),
			NeedsClarification: w.comprehension.NeedsClarification,
			Clarification:      w.comprehension.Clarification,
			Guidance:           w.plan.Guidance,
			TaskResult:         w.taskResult,
		}
	}

	result, err := chat.ExecuteChat(
		ctx, session, ac, llmConfig,
		draftID, w.comprehension.ReadMessageRange,
		triggerOverride, comprehensionInput,
	)

	if err != nil {
		if ctx.Err() != nil {
			applogger.Info("work cancelled during pipeline", "work_id", w.ID)
			w.abandon()
			return
		}
		applogger.Error("Chat processing failed in work",
			"work_id", w.ID,
			"session_id", w.sessionID,
			"error", err,
		)
		w.handleChatError()
		return
	}

	if w.draft != nil {
		w.updateDraftContent(result.Content)
	}

	w.commitDraft(result.Content)
}

func historySegments(search *comprehend.HistorySearch) []comprehend.Segment {
	if search == nil {
		return nil
	}
	return search.Segments
}

func kbSegments(retrieval *comprehend.KBRetrieval) []comprehend.Segment {
	if retrieval == nil {
		return nil
	}
	return retrieval.Segments
}

// commitDraft commits the draft by sending it through the serialized commit channel.
// The commit handler creates a message from the draft content and pushes it to SSE clients.
func (w *work) commitDraft(content string) {
	if w.draft == nil {
		applogger.Error("work.commitDraft called with nil draft", "work_id", w.ID)
		return
	}

	w.agent.draftCommitCh <- &draftCommitRequest{
		draft:     w.draft,
		sessionID: w.sessionID,
		content:   content,
	}
}

// updateDraftContent writes content to the draft in the database.
func (w *work) updateDraftContent(content string) {
	if w.draft == nil {
		return
	}
	w.draft.Content = content
	if err := database.DB.Model(&model.MessageDraft{}).Where("id = ?", w.draft.ID).
		Update("content", content).Error; err != nil {
		applogger.Error("work: failed to update draft content", "draft_id", w.draft.ID, "error", err)
	}
}

// abandon marks the work as abandoned and discards the draft.
// This is the fallback mechanism for when the guidance channel fails
// (e.g., TaskLoop unresponsive, channel full) or when the work's context
// is cancelled. Normal cancellation goes through FeedGuidance, allowing
// the TaskLoop's LLM to wrap up gracefully. This method is the safety net.
//
// Directly sets status to Abandoned in DB. The defer in Run() will not
// overwrite it because it only transitions from Running → Completed.
func (w *work) abandon() {
	if err := database.DB.Model(&model.Work{}).Where("id = ?", w.ID).
		Update("status", model.WorkStatusAbandoned).Error; err != nil {
		applogger.Error("work: failed to mark work as abandoned", "work_id", w.ID, "error", err)
	}

	if w.draft != nil {
		if err := database.DB.Model(&model.MessageDraft{}).Where("id = ?", w.draft.ID).
			Update("status", model.DraftStatusDiscarded).Error; err != nil {
			applogger.Error("work: failed to discard draft on abandon", "draft_id", w.draft.ID, "error", err)
		}
	}
}

// loadChatDependencies loads session, agent config, and LLM config from the database.
// Uses w.agent.agentConfigID (agent config PK from runtime) rather than the removed
// sessions.agent_id column.
func (w *work) loadChatDependencies() (*model.Session, *model.AgentConfig, *model.LLMConfig) {
	session, err := dops.GetSession(w.sessionID)
	if err != nil {
		applogger.Error("get session error", "session_id", w.sessionID, "reason", err)
		return nil, nil, nil
	}

	ac, err := dops.Get[model.AgentConfig](w.agent.agentConfigID)
	if err != nil {
		applogger.Error("Failed to load agent config", "agent_config_id", w.agent.agentConfigID, "error", err)
		return session, nil, nil
	}

	llmConfig, err := dops.GetLLMConfig(ac.LLMConfigID)
	if err != nil {
		applogger.Error("Failed to load LLM config", "config_id", ac.LLMConfigID, "error", err)
		return session, ac, nil
	}

	return session, ac, llmConfig
}

func (w *work) getTrigger() string {
	if w.plan != nil && w.plan.Metadata != nil && w.plan.Metadata.SessionMeta != nil {
		return w.plan.Metadata.SessionMeta.Trigger
	}
	return "work execution"
}

// handleChatError handles errors during chat processing by committing
// the draft with an error message.
func (w *work) handleChatError() {
	if w.draft != nil {
		w.commitDraft(userFriendlyErrorMsg)
	}
}

// removeWorkByID removes a work from the active works slice by its ID.
func removeWorkByID(works []*work, workID int64) []*work {
	for i, w := range works {
		if w.ID == workID {
			return append(works[:i], works[i+1:]...)
		}
	}
	return works
}

// recoverActiveWorks loads active works from the database for agent recovery
// after a service restart. Abandoned works are marked and no new Work objects
// are returned since mid-execution resumption is not supported.
func recoverActiveWorks(agentConfigID int64) []*work {
	// Resolve personID from agentConfigID — the works table is now keyed by person_id.
	ac, err := dops.Get[model.AgentConfig](agentConfigID)
	if err != nil {
		applogger.Error("recoverActiveWorks: failed to resolve person ID from agent config", "agent_config_id", agentConfigID, "error", err)
		return nil
	}
	personID := ac.PersonID

	var workRecords []model.Work
	if err := database.DB.Where("person_id = ? AND status = ?", personID, model.WorkStatusRunning).Find(&workRecords).Error; err != nil {
		applogger.Error("recoverActiveWorks: failed to load work records", "agent_config_id", agentConfigID, "error", err)
		return nil
	}

	for _, wr := range workRecords {
		// Mark recovered works as abandoned since we can't resume mid-execution
		if err := database.DB.Model(&model.Work{}).Where("id = ?", wr.ID).
			Update("status", model.WorkStatusAbandoned).Error; err != nil {
			applogger.Error("recoverActiveWorks: failed to mark work as abandoned", "work_id", wr.ID, "error", err)
		}

		if wr.DraftID != 0 {
			if err := database.DB.Model(&model.MessageDraft{}).Where("id = ?", wr.DraftID).
				Update("status", model.DraftStatusDiscarded).Error; err != nil {
				applogger.Error("recoverActiveWorks: failed to discard draft", "draft_id", wr.DraftID, "error", err)
			}
		}

		// Reset participant status to idle so the frontend doesn't show stuck "responding"
		if err := database.DB.Model(&model.ParticipantSession{}).
			Where("session_id = ? AND participant_id = ?",
				wr.SessionID, personID).
			Update("status", model.ParticipantStatusIdle).Error; err != nil {
			applogger.Error("recoverActiveWorks: failed to reset participant status",
				"session_id", wr.SessionID, "agent_config_id", agentConfigID, "error", err)
		}

		applogger.Info("Recovered work marked as abandoned",
			"work_id", wr.ID,
			"agent_config_id", agentConfigID,
			"session_id", wr.SessionID,
		)
	}

	return nil
}
