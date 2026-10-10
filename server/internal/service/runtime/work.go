package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/notification"
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/agent"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/focusedwork"
	"qingqiu-world-server/internal/service/kb"
	"qingqiu-world-server/internal/service/memory"
	"qingqiu-world-server/internal/service/tools"
)

// work represents a unit of focused-work execution for an agent.
// It is created when the agent decides to StartFocusedWork, and it may absorb
// subsequent events (e.g., guidance, cancellation) during its execution.
//
// Two-layer model: Agent (long-lived) → Work (coherent goal with ReAct loop).
//
// work holds a action.WorkPlan (carrying the Decide phase's execution intent as
// Guidance) and comprehension results (carrying the Comprehend phase's
// understanding). This ensures the execution layer has full context
// without re-interpreting the event.
type work struct {
	ID                int64
	agent             *agentRuntime
	sessionID         int64
	workspaceID       int64            // Default Workspace chosen by StartFocusedWork.
	plan              *action.WorkPlan // From Decide phase: guidance
	maxIterations     int
	focusContext      string                             // Runtime-selected prior handoffs and shared session notes
	comprehension     *comprehendTypes.Comprehension     // Results from the Comprehend phase
	focusedWorkResult *focusedwork.FocusedWorkResult     // Focused-work result
	guidanceCh        chan focusedwork.GuidanceDirective // Channel for sending guidance/cancel directives to FocusedLoop
	done              chan struct{}                      // Closed when work finishes (normal or abandoned)
	cancelMu          sync.Mutex                         // Serializes cancellation with terminal status persistence
	cancelRun         context.CancelFunc                 // Cancels only this Work's Focus execution
	cancelActionID    int64                              // Explicit Cancel Action that stopped this Work, if any
	cancelReason      string                             // The Cancel Action's reason for the terminal handoff
	executionEnded    bool                               // Protected by cancelMu; rejects late Route effects.

	// triggerAction carries the originating Action's cognitive context for the
	// WorkCompleted event (provenance only — Background and Reason).
	triggerAction *eventqueue.TriggerAction

	// startedAt is set when work begins running, read by buildActiveWorksContext
	// so the Decide LLM can see how long a work has been running.
	startedAt time.Time // Set in Run(), read by Decide
}

// Run executes focused work using Guidance from the Decide phase.
// On completion, sends a WorkCompleted event to the event loop and
// signals removal from active works.
// Respects context cancellation: exits early if the work is cancelled.
func (w *work) Run(ctx context.Context) {
	workCtx, cancel := w.executionContext(ctx)
	defer cancel()
	w.startedAt = time.Now() // Record start time for Decide-phase duration awareness
	defer close(w.done)      // Signal completion regardless of how work exits
	if err := database.DB.Model(&model.Work{}).Where("id = ? AND status = ?", w.ID, model.WorkStatusRunning).
		Updates(map[string]interface{}{"focus_phase": model.FocusPhaseExecuting, "checkpoint": "Focus is executing its current guidance."}).Error; err != nil {
		applogger.Error("work: failed to mark focus executing", "work_id", w.ID, "error", err)
	}

	defer func() {
		w.cancelMu.Lock()
		w.executionEnded = true
		// Finalize the DB status from the real outcome: a focused-work-reported
		// failure must not be recorded as Completed. The update only applies
		// when the work is still Running — cancellation may have already set
		// Abandoned, in which case this is a no-op.
		finalStatus := model.WorkStatusCompleted
		interrupted := workCtx.Err() != nil && (w.focusedWorkResult == nil || w.focusedWorkResult.Status != "success") && w.cancelActionID == 0
		if interrupted {
			finalStatus = model.WorkStatusAbandoned
		} else if w.focusedWorkResult != nil && w.focusedWorkResult.Status != "success" {
			finalStatus = model.WorkStatusFailed
		}
		finalPhase, finalCheckpoint := focusTerminalState(finalStatus, w.focusedWorkResult)
		if interrupted {
			finalPhase = model.FocusPhasePaused
			finalCheckpoint = "Focus was interrupted before completion."
		}
		if err := database.DB.Model(&model.Work{}).
			Where("id = ? AND status = ?", w.ID, model.WorkStatusRunning).
			Updates(map[string]interface{}{"status": finalStatus, "focus_phase": finalPhase, "checkpoint": finalCheckpoint}).Error; err != nil {
			applogger.Error("work: failed to update work status", "work_id", w.ID, "error", err)
		}

		// Re-read the final status from DB. Cancellation may have set Abandoned
		// while focusedWorkResult is nil (e.g. cancelled before the pipeline), so the
		// in-memory result alone cannot be trusted to derive the outcome.
		var workRow model.Work
		if err := database.DB.Select("status", "focus_phase").First(&workRow, w.ID).Error; err != nil {
			w.cancelMu.Unlock()
			applogger.Error("work: failed to load final status for memory event",
				"work_id", w.ID, "error", err)
			return
		}
		cancelActionID, cancelReason := w.cancelActionID, w.cancelReason
		w.cancelMu.Unlock()

		// Derive the outcome from the real final DB status, not from focusedWorkResult
		// alone, so abandoned works are never misreported as success.
		status := "success"
		var output, workErr string
		switch workRow.Status {
		case model.WorkStatusAbandoned:
			status = "abandoned"
		case model.WorkStatusFailed:
			status = "failure"
			if w.focusedWorkResult != nil {
				workErr = w.focusedWorkResult.Error
			}
		case model.WorkStatusCompleted:
			if w.focusedWorkResult != nil {
				output = w.focusedWorkResult.Output
			}
		default:
			applogger.Error("work: final status is not terminal; skipping terminal side effects",
				"work_id", w.ID, "status", workRow.Status)
			return
		}
		applogger.Info("work ended", "work_id", w.ID, "session_id", w.sessionID,
			"status", status, "cancel_action_id", cancelActionID)

		persistWorkHandoff(w, workRow.Status, workRow.FocusPhase, output, workErr, cancelReason)
		if err := kb.EnqueueFocusRelationAnalysisJob(w.ID); err != nil {
			applogger.Error("work: failed to enqueue focus relation analysis", "work_id", w.ID, "error", err)
		}

		// The vector queue uses this episodic gist. Durable result text is
		// retained in the Focus handoff created above, while the Event keeps
		// the Work reference and the completion time.
		gist := fmt.Sprintf("Guidance: %s\nStatus: %s\nDuration: %s",
			w.plan.Guidance, status, time.Since(w.startedAt).Truncate(time.Second))
		if output != "" {
			truncated, _ := tools.TruncateHead(output, tools.DefaultTruncateBytes)
			gist += "\nOutput: " + truncated
		}
		if workErr != "" {
			truncated, _ := tools.TruncateHead(workErr, tools.DefaultTruncateBytes)
			gist += "\nError: " + truncated
		}
		if cancelActionID > 0 {
			gist += "\nCancellation reason: " + cancelReason
		}

		// Persist the episodic memory event; eventID links the eventqueue
		// event to the memory event so the runtime can create an observation.
		eventID, err := memory.RecordWorkCompletedEvent(w.ID, gist)
		if err != nil {
			applogger.Error("work: failed to record work-completed memory event",
				"work_id", w.ID, "error", err)
			return
		}
		person, personErr := dops.GetPerson(w.agent.agentPersonID)
		if personErr != nil {
			applogger.Error("work: failed to check owner before result delivery", "work_id", w.ID, "error", personErr)
			return
		}
		if person.Status != model.PersonStatusActive {
			return
		}

		// Keep the recipient explicit until its observation is processed. A
		// restart can replay this buffer without manufacturing another Event.
		outgoing := &eventqueue.AgentEvent{
			Type:          eventqueue.EventTypeWorkCompleted,
			SessionID:     w.sessionID,
			EventID:       eventID,
			TriggerAction: w.triggerAction,
			Payload: &eventqueue.WorkCompletedPayload{
				WorkID:         w.ID,
				Guidance:       w.plan.Guidance,
				Status:         status,
				WorkOutput:     output,
				WorkError:      workErr,
				CancelActionID: cancelActionID,
				CancelReason:   cancelReason,
			},
		}
		encoded, err := serializeEventPayload(outgoing)
		if err != nil {
			applogger.Error("work: failed to encode result delivery", "work_id", w.ID, "event_id", eventID, "error", err)
			return
		}
		if err := database.DB.Transaction(func(tx *gorm.DB) error {
			if err := dops.RequireActivePersonTx(tx, w.agent.agentPersonID); err != nil {
				return err
			}
			return tx.Create(&model.AgentEventBuffer{PersonID: w.agent.agentPersonID, EventType: int(outgoing.Type), SessionID: w.sessionID, EventID: eventID, PayloadJSON: encoded}).Error
		}); err != nil {
			applogger.Error("work: failed to persist result delivery", "work_id", w.ID, "event_id", eventID, "error", err)
			return
		}
		eventqueue.SendEvent(w.agent.agentConfigID, outgoing)
	}()

	applogger.Info("work started",
		"work_id", w.ID,
		"session_id", w.sessionID,
		"guidance", w.plan.Guidance,
	)

	// Check cancellation before starting
	if workCtx.Err() != nil {
		applogger.Info("work interrupted before pipeline", "work_id", w.ID)
		w.cancelMu.Lock()
		explicitCancel := w.cancelActionID > 0
		w.cancelMu.Unlock()
		if !explicitCancel {
			if err := database.DB.Model(&model.Work{}).Where("id = ? AND status = ?", w.ID, model.WorkStatusRunning).
				Updates(map[string]interface{}{"status": model.WorkStatusAbandoned, "focus_phase": model.FocusPhasePaused,
					"checkpoint": "Focus was interrupted before it began."}).Error; err != nil {
				applogger.Error("work: failed to record pre-start interruption", "work_id", w.ID, "error", err)
			}
		}
		return
	}

	w.runFocusedWork(workCtx)
	w.cancelMu.Lock()
	w.executionEnded = true
	w.cancelMu.Unlock()

}

// executionContext gives this Work a cancellable Focus context and applies a
// Cancel Action that arrived before Run began.
func (w *work) executionContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	w.cancelMu.Lock()
	w.cancelRun = cancel
	if w.cancelActionID > 0 {
		cancel()
	}
	w.cancelMu.Unlock()
	return ctx, cancel
}

// focusTerminalState maps a terminal Work status to its runtime-owned Focus checkpoint.
func focusTerminalState(workStatus model.WorkStatus, result *focusedwork.FocusedWorkResult) (model.FocusPhase, string) {
	if workStatus == model.WorkStatusCompleted {
		return model.FocusPhaseCompleted, "Focus completed. See the handoff for its final result."
	}
	if workStatus == model.WorkStatusAbandoned {
		return model.FocusPhaseCancelled, "Focus was cancelled or abandoned before normal completion."
	}
	if result != nil && strings.Contains(strings.ToLower(result.Error), "max iterations") {
		return model.FocusPhasePaused, "Focus paused at its iteration budget. See notes and handoff before resuming."
	}
	return model.FocusPhaseFailed, "Focus failed. See the handoff for the recorded error and next step."
}

// persistWorkHandoff records compact runtime-owned continuity metadata. It
// intentionally does not attribute the shared session notes to this Work.
func persistWorkHandoff(w *work, workStatus model.WorkStatus, phase model.FocusPhase, output, workErr, cancelReason string) {
	handoffStatus := model.FocusHandoffCompleted
	unresolved := ""
	nextStep := ""
	switch workStatus {
	case model.WorkStatusFailed:
		handoffStatus = model.FocusHandoffFailed
		unresolved = workErr
		nextStep = "Investigate the recorded failure before retrying."
		if strings.Contains(strings.ToLower(workErr), "max iterations") {
			handoffStatus = model.FocusHandoffPaused
			nextStep = "Review the saved notes and handoff, then decide whether to resume this focus."
		}
	case model.WorkStatusAbandoned:
		if phase == model.FocusPhasePaused {
			handoffStatus = model.FocusHandoffInterrupted
			unresolved = "Focus was interrupted before completion."
			nextStep = "Review the durable Work records before deciding whether to continue."
		} else {
			handoffStatus = model.FocusHandoffCancelled
			unresolved = "The work was abandoned before a normal completion."
			nextStep = "Review the Work context before deciding whether to continue."
		}
		if cancelReason != "" {
			unresolved = "The work stopped after a Cancel Action: " + cancelReason
			nextStep = "Wait for a later instruction before resuming this work."
		}
	}
	summary := output
	if summary == "" {
		summary = unresolved
	}
	if summary == "" {
		summary = "No additional result was recorded."
	}
	if truncated, changed := tools.TruncateHead(summary, tools.DefaultTruncateBytes); changed {
		summary = truncated
	}
	confirmedFindings := confirmedFindingsForWork(workStatus, summary)
	artifactReferences := extractHandoffSection(summary, "artifacts", "artifact", "产物", "交付")
	if cancelReason == "" {
		if extracted := extractHandoffSection(summary, "unresolved", "未决", "未解决"); extracted != "" {
			unresolved = extracted
		}
		if extracted := extractHandoffSection(summary, "next step", "next", "下一步"); extracted != "" {
			nextStep = extracted
		}
	}
	record := &model.FocusHandoff{
		PersonID:           w.agent.agentPersonID,
		SessionID:          w.sessionID,
		WorkID:             w.ID,
		Source:             model.FocusSourceExternal,
		Status:             handoffStatus,
		Orientation:        w.plan.Guidance,
		Summary:            summary,
		ConfirmedFindings:  confirmedFindings,
		ArtifactReferences: artifactReferences,
		Unresolved:         unresolved,
		NextStep:           nextStep,
	}
	if err := dops.CreateFocusHandoff(record); err != nil {
		applogger.Error("work: failed to persist focus handoff", "work_id", w.ID, "error", err)
		return
	}
	refreshMemorySource(model.MemorySourceFocusHandoff, record.ID)
}

// confirmedFindingsForWork keeps terminal output separate from confirmed
// findings. A failed or paused Focus must not promote its partial output.
func confirmedFindingsForWork(workStatus model.WorkStatus, summary string) string {
	if workStatus != model.WorkStatusCompleted {
		return ""
	}
	if extracted := extractHandoffSection(summary, "confirmed findings", "confirmed", "已确认", "确认结果"); extracted != "" {
		return extracted
	}
	return summary
}

// extractHandoffSection reads one compact final-output section. It accepts
// English and Chinese headings while preserving the original content.
func extractHandoffSection(content string, labels ...string) string {
	lines := strings.Split(content, "\n")
	collecting := false
	var collected []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimLeft(line, "#-* "))
		lower := strings.ToLower(trimmed)
		matched := ""
		for _, label := range labels {
			if strings.HasPrefix(lower, label+":") || strings.HasPrefix(lower, label+"：") {
				matched = label
				break
			}
		}
		if matched != "" {
			collecting = true
			value := ""
			separator, width := strings.Index(trimmed, ":"), len(":")
			if chineseSeparator := strings.Index(trimmed, "："); chineseSeparator >= 0 && (separator < 0 || chineseSeparator < separator) {
				separator, width = chineseSeparator, len("：")
			}
			if separator >= 0 {
				value = strings.TrimSpace(trimmed[separator+width:])
			}
			if value != "" {
				collected = append(collected, value)
			}
			continue
		}
		if collecting && isHandoffHeading(lower) {
			break
		}
		if collecting && trimmed != "" {
			collected = append(collected, trimmed)
		}
	}
	return strings.TrimSpace(strings.Join(collected, "\n"))
}

// isHandoffHeading reports whether a line begins a recognized handoff section.
func isHandoffHeading(line string) bool {
	for _, label := range []string{"confirmed findings", "confirmed", "artifacts", "artifact", "unresolved", "next step", "next", "已确认", "确认结果", "产物", "交付", "未决", "未解决", "下一步"} {
		if strings.HasPrefix(line, label+":") || strings.HasPrefix(line, label+"：") {
			return true
		}
	}
	return false
}

// runFocusedWork executes the focused-work path using Guidance from the Decide phase.
func (w *work) runFocusedWork(ctx context.Context) {
	// Fetch agent info at the point of use — do not hold the pointer across
	// the long-running focused-work execution.
	a, err := agent.GetAgent(w.agent.agentPersonID)
	if err != nil {
		applogger.Error("runFocusedWork: failed to load agent", "person_id", w.agent.agentPersonID, "error", err)
		w.abandon()
		return
	}

	notify(notification.AgentProcessingStarted{SessionID: w.sessionID})

	w.focusedWorkResult = focusedwork.RunFocusedWork(focusedwork.RunFocusedWorkParams{
		LLMConfig:    &a.LLM,
		SessionID:    w.sessionID,
		WorkspaceID:  w.workspaceID,
		PersonID:     a.Person.ID,
		WorkID:       w.ID,
		Guidance:     w.plan.Guidance,
		Background:   w.triggerAction.Background,
		FocusContext: w.focusContext,
		FocusPhase:   model.FocusPhaseExecuting,
		Checkpoint:   "Focus is executing its current guidance.",
		Metadata:     w.plan.Metadata,
		Ctx:          ctx,
		GuidanceCh:   w.guidanceCh,
	})

}

// FeedGuidance sends a routed directive to the running Focus. Cancellation
// uses requestCancel because a stop request must not depend on another LLM turn.
func (w *work) FeedGuidance(directive focusedwork.GuidanceDirective) bool {
	w.cancelMu.Lock()
	defer w.cancelMu.Unlock()
	if w.guidanceCh == nil {
		applogger.Error("FeedGuidance called on work with nil guidanceCh",
			"work_id", w.ID,
		)
		return false
	}
	if w.executionEnded || w.cancelActionID > 0 {
		applogger.Error("FeedGuidance rejected after Work stopped", "work_id", w.ID)
		return false
	}
	var state model.Work
	if err := database.DB.Select("status").First(&state, w.ID).Error; err != nil || state.Status != model.WorkStatusRunning {
		applogger.Error("FeedGuidance rejected for non-running Work", "work_id", w.ID, "error", err)
		return false
	}
	select {
	case w.guidanceCh <- directive:
		checkpoint := fmt.Sprintf("Latest routed guidance: %s", directive.Guidance)
		if err := database.DB.Model(&model.Work{}).Where("id = ?", w.ID).
			Updates(map[string]interface{}{"focus_phase": model.FocusPhaseExecuting, "checkpoint": checkpoint}).Error; err != nil {
			applogger.Error("work: failed to update focus checkpoint from guidance", "work_id", w.ID, "error", err)
		}
		applogger.Info("Guidance fed to work",
			"work_id", w.ID,
			"guidance", directive.Guidance,
			"reason", directive.Reason,
		)
		return true
	default:
		applogger.Error("work guidanceCh full, dropping guidance",
			"work_id", w.ID,
			"guidance", directive.Guidance,
		)
		return false
	}
}

// requestCancel atomically marks an active Work as abandoned and interrupts
// its Focus context. A running tool may finish, but no later tool call or LLM
// iteration may begin from the cancelled context.
func (w *work) requestCancel(act action.Action) bool {
	w.cancelMu.Lock()
	defer w.cancelMu.Unlock()
	if w.executionEnded {
		applogger.Error("work: cancellation arrived after execution ended", "work_id", w.ID, "action_id", act.ID)
		return false
	}
	reason := strings.TrimSpace(act.Reason)
	if reason == "" {
		reason = strings.TrimSpace(act.Background)
	}
	if reason == "" && act.WorkGuidance != nil {
		reason = strings.TrimSpace(act.WorkGuidance.Guidance)
	}
	if reason == "" {
		applogger.Error("work: cancel action has no reason", "work_id", w.ID, "action_id", act.ID)
		reason = "Explicit cancellation requested."
	}
	result := database.DB.Model(&model.Work{}).
		Where("id = ? AND status = ?", w.ID, model.WorkStatusRunning).
		Updates(map[string]interface{}{
			"status":      model.WorkStatusAbandoned,
			"focus_phase": model.FocusPhaseCancelled,
			"checkpoint":  "Cancellation requested: " + reason,
		})
	if result.Error != nil {
		applogger.Error("work: failed to cancel running work", "work_id", w.ID, "action_id", act.ID, "error", result.Error)
		return false
	}
	if result.RowsAffected == 0 {
		applogger.Error("work: cancel target was no longer running", "work_id", w.ID, "action_id", act.ID)
		return false
	}
	w.cancelActionID = act.ID
	w.cancelReason = reason
	if w.cancelRun != nil {
		w.cancelRun()
	}
	applogger.Info("work cancellation requested", "work_id", w.ID, "action_id", act.ID, "reason", reason)
	return true
}

// abandon is the fallback for a Work that cannot enter or finish Focus.
// Its update is limited to a running Work so it cannot overwrite a terminal
// result. The defer in Run() also finalizes only from the running state.
func (w *work) abandon() {
	if err := database.DB.Model(&model.Work{}).Where("id = ? AND status = ?", w.ID, model.WorkStatusRunning).
		Updates(map[string]interface{}{"status": model.WorkStatusAbandoned, "focus_phase": model.FocusPhaseCancelled, "checkpoint": "Focus was abandoned before normal completion."}).Error; err != nil {
		applogger.Error("work: failed to mark work as abandoned", "work_id", w.ID, "error", err)
	}
}

// loadSession loads the session for this work from the database.
func (w *work) loadSession() *model.Session {
	session, err := dops.GetSession(w.sessionID)
	if err != nil {
		applogger.Error("get session error", "session_id", w.sessionID, "reason", err)
		return nil
	}
	return session
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

// recoverActiveWorks closes interrupted Work execution and repairs missing
// handoffs/result Events. No Focus is resumed after process restart.
func recoverActiveWorks(agentConfigID int64) []*work {
	// Resolve personID from agentConfigID.
	ac, err := dops.Get[model.AgentConfig](agentConfigID)
	if err != nil {
		applogger.Error("recoverActiveWorks: failed to resolve person ID from agent config", "agent_config_id", agentConfigID, "error", err)
		return nil
	}
	personID := ac.PersonID

	var workRecords []model.Work
	// A result Event may have committed before its recipient buffer. Repair
	// both boundaries without loading every historical Work into Go memory.
	if err := database.DB.Where(`person_id = ? AND (status = ?
		OR NOT EXISTS (SELECT 1 FROM events e WHERE e.event_type = ? AND e.ref_id = works.id)
		OR NOT EXISTS (SELECT 1 FROM focus_handoffs h WHERE h.person_id = works.person_id AND h.work_id = works.id AND h.source = ?)
		OR EXISTS (SELECT 1 FROM events e WHERE e.event_type = ? AND e.ref_id = works.id
			AND NOT EXISTS (SELECT 1 FROM agent_observations o WHERE o.event_id = e.id AND o.person_id = works.person_id)
			AND NOT EXISTS (SELECT 1 FROM agent_event_buffers b WHERE b.event_id = e.id AND b.person_id = works.person_id)))`,
		personID, model.WorkStatusRunning, model.EventTypeWorkCompleted, model.FocusSourceExternal, model.EventTypeWorkCompleted).Find(&workRecords).Error; err != nil {
		applogger.Error("recoverActiveWorks: failed to load work records", "agent_config_id", agentConfigID, "error", err)
		return nil
	}

	for _, wr := range workRecords {
		if wr.Status == model.WorkStatusRunning {
			if err := database.DB.Model(&model.Work{}).Where("id = ? AND status = ?", wr.ID, model.WorkStatusRunning).
				Updates(map[string]any{"status": model.WorkStatusAbandoned, "focus_phase": model.FocusPhasePaused, "checkpoint": "Focus was interrupted before completion."}).Error; err != nil {
				applogger.Error("recoverActiveWorks: failed to mark interrupted Work", "work_id", wr.ID, "error", err)
				continue
			}
			wr.Status = model.WorkStatusAbandoned
			wr.FocusPhase = model.FocusPhasePaused
		}
		if err := repairWorkResult(personID, wr); err != nil {
			applogger.Error("recoverActiveWorks: failed to repair Work result", "work_id", wr.ID, "error", err)
		}
		if wr.SessionID > 0 {
			if err := database.DB.Model(&model.ParticipantSession{}).Where("session_id = ? AND participant_id = ?", wr.SessionID, personID).
				Update("status", model.ParticipantStatusIdle).Error; err != nil {
				applogger.Error("recoverActiveWorks: failed to reset participant status", "session_id", wr.SessionID, "agent_config_id", agentConfigID, "error", err)
			}
		}
	}

	return nil
}

// repairWorkResult is idempotent across startup retries. The Work row remains
// the factual status; missing output is explicitly described as unavailable.
func repairWorkResult(personID int64, wr model.Work) error {
	var handoff model.FocusHandoff
	err := database.DB.Where("person_id = ? AND work_id = ? AND source = ?", personID, wr.ID, model.FocusSourceExternal).Order("id DESC").Take(&handoff).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	if err == gorm.ErrRecordNotFound {
		status := model.FocusHandoffCancelled
		if wr.Status == model.WorkStatusAbandoned && wr.FocusPhase == model.FocusPhasePaused {
			status = model.FocusHandoffInterrupted
		}
		if wr.Status == model.WorkStatusCompleted {
			status = model.FocusHandoffCompleted
		}
		if wr.Status == model.WorkStatusFailed {
			status = model.FocusHandoffFailed
		}
		handoff = model.FocusHandoff{PersonID: personID, SessionID: wr.SessionID, WorkID: wr.ID, Source: model.FocusSourceExternal, Status: status,
			Orientation: wr.Description, Summary: "Result details were unavailable after service interruption.",
			Unresolved: "The original Focus result could not be recovered.", NextStep: "Reassess this Work from its durable records."}
		if wr.Status == model.WorkStatusAbandoned && status == model.FocusHandoffInterrupted {
			handoff.Summary = "Focus was interrupted before completion."
			handoff.Unresolved = handoff.Summary
		}
		if err := dops.CreateFocusHandoff(&handoff); err != nil {
			return err
		}
		refreshMemorySource(model.MemorySourceFocusHandoff, handoff.ID)
	}
	status := "abandoned"
	if wr.Status == model.WorkStatusCompleted {
		status = "success"
	}
	if wr.Status == model.WorkStatusFailed {
		status = "failure"
	}
	payload := &eventqueue.WorkCompletedPayload{WorkID: wr.ID, Guidance: wr.Description, Status: status, WorkOutput: handoff.Summary}
	var eventID int64
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		var event model.Event
		lookup := tx.Where("event_type = ? AND ref_id = ?", model.EventTypeWorkCompleted, wr.ID).Order("id").Take(&event).Error
		if lookup != nil && lookup != gorm.ErrRecordNotFound {
			return lookup
		}
		if lookup == gorm.ErrRecordNotFound {
			event = model.Event{EventType: model.EventTypeWorkCompleted, RefID: wr.ID}
			if err := tx.Create(&event).Error; err != nil {
				return err
			}
		}
		eventID = event.ID
		var person model.Person
		if err := tx.Select("status").First(&person, personID).Error; err != nil {
			return err
		}
		if person.Status != model.PersonStatusActive {
			return nil
		}
		var observed int64
		if err := tx.Model(&model.AgentObservation{}).Where("person_id = ? AND event_id = ?", personID, eventID).Count(&observed).Error; err != nil {
			return err
		}
		if observed > 0 {
			return nil
		}
		var buffered int64
		if err := tx.Model(&model.AgentEventBuffer{}).Where("person_id = ? AND event_id = ?", personID, eventID).Count(&buffered).Error; err != nil {
			return err
		}
		if buffered > 0 {
			return nil
		}
		outgoing := &eventqueue.AgentEvent{Type: eventqueue.EventTypeWorkCompleted, SessionID: wr.SessionID, EventID: eventID, Payload: payload}
		encoded, err := serializeEventPayload(outgoing)
		if err != nil {
			return err
		}
		return tx.Create(&model.AgentEventBuffer{PersonID: personID, EventType: int(outgoing.Type), SessionID: wr.SessionID, EventID: eventID, PayloadJSON: encoded}).Error
	})
	if err != nil {
		return err
	}
	refreshMemorySource(model.MemorySourceEvent, eventID)
	return nil
}

// recoverDeceasedWorks closes Work execution that cannot resume after its
// owner died. It records objective interruption history without delivery.
func recoverDeceasedWorks() {
	var works []model.Work
	if err := database.DB.Where(`person_id IN (SELECT id FROM persons WHERE status = ?) AND (status = ?
		OR NOT EXISTS (SELECT 1 FROM events e WHERE e.event_type = ? AND e.ref_id = works.id)
		OR NOT EXISTS (SELECT 1 FROM focus_handoffs h WHERE h.person_id = works.person_id AND h.work_id = works.id AND h.source = ?))`,
		model.PersonStatusDeceased, model.WorkStatusRunning, model.EventTypeWorkCompleted, model.FocusSourceExternal).Find(&works).Error; err != nil {
		applogger.Error("recoverDeceasedWorks: failed to list interrupted Works", "error", err)
		return
	}
	for _, wr := range works {
		if err := database.DB.Model(&model.Work{}).Where("id = ? AND status = ?", wr.ID, model.WorkStatusRunning).
			Updates(map[string]any{"status": model.WorkStatusAbandoned, "focus_phase": model.FocusPhasePaused, "checkpoint": "Owner died before Focus completed."}).Error; err != nil {
			applogger.Error("recoverDeceasedWorks: failed to close Work", "work_id", wr.ID, "error", err)
			continue
		}
		wr.Status = model.WorkStatusAbandoned
		wr.FocusPhase = model.FocusPhasePaused
		if err := repairWorkResult(wr.PersonID, wr); err != nil {
			applogger.Error("recoverDeceasedWorks: failed to record interruption", "work_id", wr.ID, "error", err)
		}
	}
}
