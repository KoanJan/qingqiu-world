package runtime

import (
	"context"
	"fmt"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/notification"
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/agent"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/memory"
	"qingqiu-world-server/internal/service/task"
	"qingqiu-world-server/internal/service/tools"
)

// work represents a unit of task execution for an agent.
// It is created when the agent decides to CreateTask, and it may absorb
// subsequent events (e.g., guidance, cancellation) during its execution.
//
// Two-layer model: Agent (long-lived) → Work (coherent goal with ReAct loop).
//
// work holds a action.WorkPlan (carrying the Decide phase's execution intent as
// Guidance) and comprehension results (carrying the Comprehend phase's
// understanding). This ensures the execution layer has full context
// without re-interpreting the event.
type work struct {
	ID            int64
	agent         *agentRuntime
	sessionID     int64
	plan          *action.WorkPlan // From Decide phase: guidance
	maxIterations int
	comprehension *comprehendTypes.Comprehension // Results from the Comprehend phase
	taskResult    *task.TaskResult               // Task execution result
	guidanceCh    chan task.GuidanceDirective    // Channel for sending guidance/cancel directives to TaskLoop
	done          chan struct{}                  // Closed when work finishes (normal or abandoned)

	// triggerAction carries the originating Action's cognitive context for the
	// WorkCompleted event (provenance only — Background and Reason).
	triggerAction *eventqueue.TriggerAction

	// startedAt is set when work begins running, read by buildActiveWorksContext
	// so the Decide LLM can see how long a work has been running.
	startedAt time.Time // Set in Run(), read by Decide
}

// Run executes the task work using Guidance from the Decide phase.
// On completion, sends a WorkCompleted event to the event loop and
// signals removal from active works.
// Respects context cancellation: exits early if the work is cancelled.
func (w *work) Run(ctx context.Context) {
	w.startedAt = time.Now() // Record start time for Decide-phase duration awareness
	defer close(w.done)      // Signal completion regardless of how work exits

	defer func() {
		// Finalize the DB status from the real outcome: a task-reported
		// failure must not be recorded as Completed. The update only applies
		// when the work is still Running — abandon() may have already set
		// Abandoned, in which case this is a no-op.
		finalStatus := model.WorkStatusCompleted
		if w.taskResult != nil && w.taskResult.Status != "success" {
			finalStatus = model.WorkStatusFailed
		}
		if err := database.DB.Model(&model.Work{}).
			Where("id = ? AND status = ?", w.ID, model.WorkStatusRunning).
			Update("status", finalStatus).Error; err != nil {
			applogger.Error("work: failed to update work status", "work_id", w.ID, "error", err)
		}

		// Re-read the final status from DB. abandon() may have set Abandoned
		// while taskResult is nil (e.g. cancelled before the pipeline), so the
		// in-memory result alone cannot be trusted to derive the outcome.
		var workRow model.Work
		if err := database.DB.Select("status").First(&workRow, w.ID).Error; err != nil {
			applogger.Error("work: failed to load final status for memory event",
				"work_id", w.ID, "error", err)
		}

		// Derive the outcome from the real final DB status, not from taskResult
		// alone, so abandoned works are never misreported as success.
		status := "success"
		var output, taskErr string
		switch workRow.Status {
		case model.WorkStatusAbandoned:
			status = "abandoned"
		case model.WorkStatusFailed:
			status = "failure"
			if w.taskResult != nil {
				taskErr = w.taskResult.Error
			}
		default:
			if w.taskResult != nil {
				output = w.taskResult.Output
			}
		}

		// Episodic gist for the memory event. The works row only carries
		// description and status, so this text is the retrievable content.
		// TaskOutput is used as-is (head-truncated) without re-summarization.
		gist := fmt.Sprintf("Guidance: %s\nStatus: %s\nDuration: %s",
			w.plan.Guidance, status, time.Since(w.startedAt).Truncate(time.Second))
		if output != "" {
			truncated, _ := tools.TruncateHead(output, tools.DefaultTruncateBytes)
			gist += "\nOutput: " + truncated
		}
		if taskErr != "" {
			truncated, _ := tools.TruncateHead(taskErr, tools.DefaultTruncateBytes)
			gist += "\nError: " + truncated
		}

		// Persist the episodic memory event; eventID links the eventqueue
		// event to the memory event so the runtime can create an observation.
		eventID, err := memory.RecordWorkCompletedEvent(w.ID, gist)
		if err != nil {
			applogger.Error("work: failed to record work-completed memory event",
				"work_id", w.ID, "error", err)
		}

		// Send work completed event to the agent's event queue.
		// The agent processes this through the same Comprehend->Decide pipeline
		// as external events, deciding whether to inform the user.
		eventqueue.SendEvent(w.agent.agentConfigID, &eventqueue.AgentEvent{
			Type:          eventqueue.EventTypeWorkCompleted,
			SessionID:     w.sessionID,
			EventID:       eventID,
			TriggerAction: w.triggerAction,
			Payload: &eventqueue.WorkCompletedPayload{
				WorkID:     w.ID,
				Guidance:   w.plan.Guidance,
				Status:     status,
				TaskOutput: output,
				TaskError:  taskErr,
			},
		})
	}()

	applogger.Info("work started",
		"work_id", w.ID,
		"session_id", w.sessionID,
		"guidance", w.plan.Guidance,
	)

	// Check cancellation before starting
	if ctx.Err() != nil {
		applogger.Info("work cancelled before pipeline", "work_id", w.ID)
		w.abandon()
		return
	}

	w.runTask(ctx)

	applogger.Info("work completed",
		"work_id", w.ID,
		"session_id", w.sessionID,
	)
}

// runTask executes the task path using Guidance from the Decide phase.
func (w *work) runTask(ctx context.Context) {
	session := w.loadSession()
	if session == nil {
		w.abandon()
		return
	}

	// Fetch agent info at the point of use — do not hold the pointer across
	// the long-running task execution.
	a, err := agent.GetAgent(w.agent.agentPersonID)
	if err != nil {
		applogger.Error("runTask: failed to load agent", "person_id", w.agent.agentPersonID, "error", err)
		w.abandon()
		return
	}

	notify(notification.AgentProcessingStarted{SessionID: w.sessionID})

	w.taskResult = task.RunTask(task.RunTaskParams{
		LLMConfig:  &a.LLM,
		SessionID:  w.sessionID,
		PersonID:   a.Person.ID,
		WorkID:     w.ID,
		Guidance:   w.plan.Guidance,
		Background: w.triggerAction.Background,
		Metadata:   w.plan.Metadata,
		Ctx:        ctx,
		GuidanceCh: w.guidanceCh,
	})

	applogger.Info("TaskWork completed",
		"work_id", w.ID,
		"session_id", w.sessionID,
		"status", w.taskResult.Status,
	)
}

// FeedGuidance sends a guidance directive to the work's guidance channel.
// This is called when the Decide phase routes an event to an existing
// TaskWork or cancels it - the directive becomes an environment event
// that the TaskLoop observes at the next iteration boundary.
//
// For cancel, the directive carries guidance like "save progress and stop"
// and the reason explaining why. The TaskLoop's LLM processes this and
// decides how to wrap up - this is "appealable" cancellation, not forceful kill.
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

// abandon marks the work as abandoned.
// This is the fallback mechanism for when context is cancelled or
// dependencies cannot be loaded. Normal cancellation goes through
// FeedGuidance, allowing the TaskLoop's LLM to wrap up gracefully.
// This method is the safety net.
//
// Directly sets status to Abandoned in DB. The defer in Run() will not
// overwrite it because it only transitions from Running -> Completed.
func (w *work) abandon() {
	if err := database.DB.Model(&model.Work{}).Where("id = ?", w.ID).
		Update("status", model.WorkStatusAbandoned).Error; err != nil {
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

// recoverActiveWorks loads running works from the database for agent recovery
// after a service restart. All recovered works are marked as abandoned since
// mid-execution resumption is not supported.
func recoverActiveWorks(agentConfigID int64) []*work {
	// Resolve personID from agentConfigID.
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
		// Mark recovered works as abandoned since we can't resume mid-execution.
		if err := database.DB.Model(&model.Work{}).Where("id = ?", wr.ID).
			Update("status", model.WorkStatusAbandoned).Error; err != nil {
			applogger.Error("recoverActiveWorks: failed to mark work as abandoned", "work_id", wr.ID, "error", err)
		}

		// Reset participant status to idle so the frontend doesn't show stuck "responding".
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
