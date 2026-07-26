package runtime

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/comprehend"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/task"

	applogger "qingqiu-world-server/internal/logger"
)

// ==========================================================================
// Types & Constants
// ==========================================================================

// draftCommitRequest represents a request to commit a draft to the messages table.
// Sent through commitCh to serialize message writes across concurrent Works.
type draftCommitRequest struct {
	content   string // Final content to write
	draft     *model.MessageDraft
	sessionID int64
}

// Heartbeat interval constants for the tickless three-phase model.
// Active: agent just completed interaction, context is fresh.
// Steady: session has ongoing activity but agent doesn't participate.
// Dormant: session has been idle for a long time.
const (
	heartbeatActive  = 5 * time.Minute
	heartbeatSteady  = 30 * time.Minute
	heartbeatDormant = 2 * time.Hour
	ticksToSteady    = 3 // Consecutive none → transition to steady
	ticksToDormant   = 6 // Consecutive none → transition to dormant
)

// agentRuntime is the event-driven execution engine for an agent.
// It transforms an Agent from a passive configuration object into an active,
// stateful entity with its own lifecycle.
//
// The runtime runs a single goroutine event loop (for-select + eventCh + heartbeatTimer).
// Work execution runs in separate goroutines, allowing the event loop to remain responsive.
type agentRuntime struct {
	activeWorks        []*work
	agentConfigID      int64
	agentPersonID      int64                         // Agent's PersonID for participant_session queries
	eventCh            <-chan *eventqueue.AgentEvent // Read-only channel subscribed from eventqueue.Global
	draftCommitCh      chan *draftCommitRequest
	heartbeatInterval  time.Duration                                              // Base heartbeat interval (adaptive)
	idleTicks          int                                                        // Consecutive idle heartbeats (for tickless backoff)
	heartbeatTick      int                                                        // Total heartbeat ticks (for check scheduling)
	mu                 sync.Mutex                                                 // Protects activeWrites for external queries
	learningInProgress atomic.Bool                                                // Guards against concurrent learning checks
	onStatusChange     func(agentConfigID, personID, sessionID int64, status int) // Callback for SSE push
}

// ==========================================================================
// Construction
// ==========================================================================

// newAgentRuntime creates a new runtime for an agent with minimal initialization.
// This is the internal constructor — for external use, see createAgentRuntime
// which adds event subscription and work recovery.
func newAgentRuntime(
	agentConfigID int64,
	eventCh <-chan *eventqueue.AgentEvent,
	heartbeatInterval time.Duration,
	onStatusChange func(agentConfigID, personID, sessionID int64, status int),
) *agentRuntime {
	return &agentRuntime{
		agentConfigID:     agentConfigID,
		eventCh:           eventCh,
		draftCommitCh:     make(chan *draftCommitRequest, 16),
		heartbeatInterval: heartbeatInterval,
		onStatusChange:    onStatusChange,
	}
}

// ==========================================================================
// Main Event Loop
// ==========================================================================

// Run starts the agent's event loop. This is the agent's core execution
// thread — all events (user messages, work completions, scheduled alarms)
// arrive here and flow through Comprehend→Decide→Execute.
//
// Blocks until context is cancelled. The ctx should be the runtime's
// lifecycle context, created with a cancel function stored on the struct
// for external shutdown via Stop().
func (r *agentRuntime) Run(ctx context.Context) {
	heartbeatTimer := time.NewTimer(r.heartbeatInterval)

	// Track internal goroutines (draft handler + work goroutines)
	// so that graceful shutdown can wait for them to finish.
	var internalWg sync.WaitGroup

	// Start draft-commit handler goroutine
	internalWg.Add(1)
	go func() {
		defer internalWg.Done()
		r.handleDraftCommits(ctx)
	}()

	for {
		select {
		case <-ctx.Done():
			heartbeatTimer.Stop()
			// Drain the timer channel to prevent leak if Stop returned false
			select {
			case <-heartbeatTimer.C:
			default:
			}

			// Wait for all active works to finish.
			// Each work checks ctx.Err() and abandons quickly.
			r.mu.Lock()
			pending := make([]*work, len(r.activeWorks))
			copy(pending, r.activeWorks)
			r.mu.Unlock()
			for _, w := range pending {
				<-w.done
			}

			// Wait for draft handler to drain its channel
			internalWg.Wait()

			applogger.Info("agentRuntime stopped", "agent_config_id", r.agentConfigID)
			return

		case event := <-r.eventCh:
			// Reset idle counter on event arrival
			r.idleTicks = 0

			// ── Pre-processing: acknowledge the event ──
			// Mark messages as read so the agent won't re-process them in
			// subsequent heartbeats. Trigger summary generation for new messages.
			switch event.Type {
			// Handle work completion: remove from active works and update status.
			// This is the agent's self-perception — "I just finished doing X."
			// It goes through the same Comprehend→Decide pipeline so the agent
			// can decide whether to inform the user.
			case eventqueue.EventTypeWorkCompleted:
				if payload, ok := event.Payload.(*eventqueue.WorkCompletedPayload); ok {
					r.activeWorks = removeWorkByID(r.activeWorks, payload.WorkID)
					applogger.Info("Work completed event received",
						"agent_config_id", r.agentConfigID,
						"work_id", payload.WorkID,
						"work_type", payload.WorkType,
						"status", payload.Status,
						"active_works_remaining", len(r.activeWorks),
					)
					// Only set idle if no other works are active in this session
					if !r.hasActiveWorkInSession(event.SessionID) {
						r.weakUpdateAgentStatusInSession(event.SessionID, model.ParticipantStatusIdle)
					}
				}
			// Mark the message as read immediately upon receiving the event.
			// "Read" means the agent is aware of the message — this is separate
			// from "processed" (which happens after the work completes).
			// - For EventTypeNewMessage: the actual user message.
			// - For EventTypeScheduled: the original user message that caused
			//   the alarm (TriggerMessageID), preserving the causal chain.
			case eventqueue.EventTypeNewPrivateChatMessage:
				if payload, ok := event.Payload.(*eventqueue.NewMessagePayload); ok && payload.MessageID > 0 {
					if err := database.DB.Model(&model.ParticipantSession{}).
						Where("session_id = ? AND participant_id = ?",
							event.SessionID, r.agentPersonID).
						Update("last_read_message_id", payload.MessageID).Error; err != nil {
						applogger.Error("failed to advance last_read_message_id on new message event", "error", err)
					}
				}

			case eventqueue.EventTypeScheduled:
				if payload, ok := event.Payload.(*eventqueue.ScheduledEventPayload); ok && payload.TriggerMessageID > 0 {
					if err := database.DB.Model(&model.ParticipantSession{}).
						Where("session_id = ? AND participant_id = ? AND last_read_message_id < ?",
							event.SessionID, r.agentPersonID, payload.TriggerMessageID).
						Update("last_read_message_id", payload.TriggerMessageID).Error; err != nil {
						applogger.Error("failed to advance last_read_message_id on scheduled event", "error", err)
					}
				}
				// Fast path for scheduled events with action=send_message.
				// Skip the entire LLM pipeline and directly commit the pre-computed
				// message. This is the optimization for simple reminders where the
				// agent already knows exactly what to say when setting the alarm.
				if payload, ok := event.Payload.(*eventqueue.ScheduledEventPayload); ok &&
					payload.Action == model.ScheduledEventActionSendMessage &&
					payload.ActionContent != "" {
					r.handleFastPathSendMessage(event.SessionID, payload)
					r.resetHeartbeatTimer(heartbeatTimer)
					continue
				}

			// Alarm created by a tool (wake_me_when). Register a goroutine
			// to wait for the trigger time. This is a control event — it does
			// NOT enter the Comprehend→Decide pipeline.
			case eventqueue.EventTypeAlarmCreated:
				if payload, ok := event.Payload.(*eventqueue.AlarmCreatedPayload); ok {
					r.handleAlarmCreated(payload.ScheduledEventID)
				}
				r.resetHeartbeatTimer(heartbeatTimer)
				continue

			default:
				applogger.Error("unknown event type", "event_type", event.Type)
				continue
			}

			// Decision: how should the agent respond to this event?
			// After the cognitive order refactoring, we Comprehend first,
			// then Decide based on the comprehension results.
			ac, err := dops.Get[model.AgentConfig](r.agentConfigID)
			if err != nil {
				applogger.Error("Failed to load agent config in handleEvent", "agent_config_id", r.agentConfigID, "error", err)
				continue
			}
			llmConfig, err := dops.GetLLMConfig(ac.LLMConfigID)
			if err != nil {
				applogger.Error("Failed to load LLM config in handleEvent", "agent_config_id", r.agentConfigID, "llm_config_id", ac.LLMConfigID, "error", err)
				continue
			}

			// ── Phase 1: Comprehend ──
			// Understand the event in context: who is speaking, what do they mean,
			// what's the user's state, what knowledge is relevant.
			activeWorksSummary := buildActiveWorksSummary(r.activeWorks, event.SessionID)
			comprehension := comprehend.Comprehend(ctx, event, ac, llmConfig, activeWorksSummary)

			// ── Phase 2: Decide ──
			// Based on comprehension, determine what action(s) to take.
			// A single decision can produce multiple actions: e.g., cancel an
			// old task AND create a new one.
			decision := Decide(ctx, event, ac, llmConfig, comprehension, r.activeWorks)

			// ── Phase 3: Execute ──
			// Carry out all actions from the decision.
			for _, action := range decision.Actions {
				switch action.Type {
				case ActionRoute:
					// Route the event to an existing active Work.
					// Only TaskWork supports absorbing new guidance via its ReAct loop.
					// ChatWork is one-shot (no iteration loop), so route to ChatWork is invalid.
					wg := action.WorkGuidance
					target := r.findActiveWorkByID(wg.TargetWorkID)
					if target == nil {
						applogger.Error("Target work not found for route, skipping",
							"agent_config_id", r.agentConfigID,
							"target_work_id", wg.TargetWorkID,
						)
						continue
					}
					if target.plan.Type != model.WorkTypeTask {
						applogger.Error("Route target is not TaskWork, skipping",
							"agent_config_id", r.agentConfigID,
							"work_id", target.ID,
							"work_type", target.plan.Type,
						)
						continue
					}
					target.FeedGuidance(task.GuidanceDirective{
						Guidance: wg.Guidance,
						Reason:   wg.Reason,
					})
					applogger.Info("Routed guidance to existing TaskWork",
						"agent_config_id", r.agentConfigID,
						"work_id", target.ID,
						"guidance", wg.Guidance,
						"reason", wg.Reason,
					)

				case ActionCancel:
					// Cancel an existing active Work by sending a cancel directive
					// through the guidance channel. This is "appealable" cancellation —
					// the TaskLoop's LLM receives the directive and decides how to wrap up
					// (save notes, record reasons) before exiting.
					//
					// If the guidance channel is full or the TaskLoop is unresponsive,
					// abandon() is called as a fallback to forcefully mark the work as abandoned.
					wg := action.WorkGuidance
					target := r.findActiveWorkByID(wg.TargetWorkID)
					if target == nil {
						applogger.Error("Target work not found for cancel, skipping",
							"agent_config_id", r.agentConfigID,
							"target_work_id", wg.TargetWorkID,
						)
						continue
					}
					if target.plan.Type != model.WorkTypeTask {
						// ChatWork has no iteration loop, so it can't absorb a cancel directive.
						// Fall back to direct abandon for ChatWork.
						target.abandon()
						applogger.Info("Cancelled ChatWork by direct abandon (no iteration loop)",
							"agent_config_id", r.agentConfigID,
							"work_id", wg.TargetWorkID,
						)
						continue
					}
					// Send cancel directive to TaskWork via guidance channel.
					// The TaskLoop will observe it at the next iteration boundary.
					target.FeedGuidance(task.GuidanceDirective{
						Guidance: wg.Guidance,
						Reason:   wg.Reason,
					})
					applogger.Info("Sent cancel directive to TaskWork",
						"agent_config_id", r.agentConfigID,
						"work_id", wg.TargetWorkID,
						"guidance", wg.Guidance,
						"reason", wg.Reason,
					)

				case ActionCreate:
					// Instantiate Work from the Action's WorkPlan
					if action.WorkPlan == nil {
						applogger.Error("Create action has no work_plan, skipping",
							"agent_config_id", r.agentConfigID,
						)
						continue
					}
					w, success := r.newWork(event, action.WorkPlan, comprehension)
					if !success {
						applogger.Error("failed to create work")
						continue
					}

					// For WorkCompleted events, pass the task result to the new
					// ChatWork so it can reference the execution outcome.
					if event.Type == eventqueue.EventTypeWorkCompleted {
						if payload, ok := event.Payload.(*eventqueue.WorkCompletedPayload); ok {
							w.taskResult = &task.TaskResult{
								Status: payload.Status,
								Output: payload.TaskOutput,
								Error:  payload.TaskError,
							}
						}
					}

					r.activeWorks = append(r.activeWorks, w)
					go w.Run(ctx)
				}
			}
			r.resetHeartbeatTimer(heartbeatTimer)

		case <-heartbeatTimer.C:
			r.handleHeartbeat(ctx)
			r.resetHeartbeatTimer(heartbeatTimer)
		}
	}
}

// ==========================================================================
// Work Management
// ==========================================================================

// findActiveWorkByID finds an active work by its ID.
// Returns nil if not found.
func (r *agentRuntime) findActiveWorkByID(workID int64) *work {
	for _, w := range r.activeWorks {
		if w.ID == workID {
			return w
		}
	}
	return nil
}

// newWork creates a new Work from an event, persists it to the database,
// and sets the agent status to working.
//
// After the cognitive order refactoring, WorkPlan carries the Decide phase's
// execution intent (Guidance), and comprehension carries the Comprehend
// phase's understanding. The Work has full context without re-interpreting
// the event.
func (r *agentRuntime) newWork(event *eventqueue.AgentEvent, plan *WorkPlan, comprehension *comprehend.ComprehensionResult) (*work, bool) {

	plan.Metadata = buildMetadata(event)

	// Create draft for this work, snapshotting the agent's current read position
	// as the context boundary. Messages up to this ID were visible when the
	// work started, ensuring preprocessing and context assembly have the
	// correct conversation history.
	var (
		agentLastReadID int64
		ps              *model.ParticipantSession = &model.ParticipantSession{}
		draft           *model.MessageDraft
	)

	// tx
	tx := database.DB.Begin()
	defer tx.Rollback()

	if plan.Type == model.WorkTypeChat {
		// create MessageDraft only creating chat work
		if err := tx.Where("session_id = ? AND participant_id = ?",
			event.SessionID, r.agentPersonID).First(ps).Error; err == nil {
			agentLastReadID = ps.LastReadMessageID
		}

		draft = &model.MessageDraft{
			PersonID:          r.agentPersonID,
			SessionID:         event.SessionID,
			Status:            model.DraftStatusBuilding,
			LastReadMessageID: agentLastReadID,
		}
		if err := tx.Create(draft).Error; err != nil {
			applogger.Error("Failed to create draft", "agent_config_id", r.agentConfigID, "session_id", event.SessionID, "error", err)
			return nil, false
		}
	}

	// Persist work to database
	workRecord := &model.Work{
		PersonID:    r.agentPersonID,
		SessionID:   event.SessionID,
		Type:        plan.Type,
		Description: event.FormatDescription(),
		Status:      model.WorkStatusRunning,
	}
	if plan.Type == model.WorkTypeChat {
		workRecord.DraftID = draft.ID
	}
	if err := tx.Create(workRecord).Error; err != nil {
		applogger.Error("Failed to create work", "agent_config_id", r.agentConfigID, "session_id", event.SessionID, "error", err)
		return nil, false
	}

	w := &work{
		ID:             workRecord.ID,
		agent:          r,
		sessionID:      event.SessionID,
		plan:           plan,
		initialPayload: event.Payload,
		comprehension:  comprehension,
		guidanceCh:     make(chan task.GuidanceDirective, 8), // Buffered channel for guidance/cancel directives
		done:           make(chan struct{}),
	}
	switch plan.Type {
	case model.WorkTypeChat:
		w.draft = draft
	case model.WorkTypeTask:
		w.maxIterations = 90
	}

	if err := tx.Commit().Error; err != nil {
		applogger.Error("Failed to create work", "agent_config_id", r.agentConfigID, "session_id", event.SessionID, "error", err)
		return nil, false
	}

	r.weakUpdateAgentStatusInSession(event.SessionID, model.ParticipantStatusWorking)
	return w, true
}

// buildMetadata constructs system-generated Metadata from the triggering event.
// This is used by the task loop to understand its origin (session, self-reminder, etc.)
// and to power tools like search_chat_histories with the correct session context.
func buildMetadata(event *eventqueue.AgentEvent) *task.Metadata {
	switch event.Type {
	case eventqueue.EventTypeNewPrivateChatMessage:
		if payload, ok := event.Payload.(*eventqueue.NewMessagePayload); ok {
			return &task.Metadata{
				SourceType: task.SourceTypeSession,
				SessionMeta: &task.SessionMeta{
					SessionID:        event.SessionID,
					TriggerMessageID: payload.MessageID,
					SenderName:       payload.SpeakerName,
				},
			}
		}
	case eventqueue.EventTypeScheduled:
		return &task.Metadata{
			SourceType: task.SourceTypeScheduled,
		}
	case eventqueue.EventTypeWorkCompleted:
		return &task.Metadata{
			SourceType: task.SourceTypeWorkCompleted,
			SessionMeta: &task.SessionMeta{
				SessionID: event.SessionID,
			},
		}
	}
	return nil
}

// ==========================================================================
// Fast Path
// ==========================================================================

// handleFastPathSendMessage handles the fast path for scheduled events with
// action=send_message. It directly creates a message with the pre-computed
// content, skipping the entire LLM pipeline (no context engineering, no
// inference, no tool calls). This is the optimization for simple reminders.
//
// The method still creates a draft (for audit trail) and commits through the
// serialized commitCh to maintain message ordering. No Work object is created,
// so the agent status transitions are handled inline:
//   - working → (commit) → idle
func (r *agentRuntime) handleFastPathSendMessage(sessionID int64, payload *eventqueue.ScheduledEventPayload) {
	applogger.Info("Fast path: sending pre-computed message for scheduled event",
		"agent_config_id", r.agentConfigID,
		"session_id", sessionID,
		"scheduled_event_id", payload.ScheduledEventID,
	)

	// Get agent's current read position
	var agentLastReadID int64
	var ps model.ParticipantSession
	if err := database.DB.Where("session_id = ? AND participant_id = ?",
		sessionID, r.agentPersonID).First(&ps).Error; err == nil {
		agentLastReadID = ps.LastReadMessageID
	}

	// Create draft for audit trail
	draft := &model.MessageDraft{
		PersonID:          r.agentPersonID,
		SessionID:         sessionID,
		Status:            model.DraftStatusBuilding,
		LastReadMessageID: agentLastReadID,
	}
	if err := database.DB.Create(draft).Error; err != nil {
		applogger.Error("Failed to create draft for fast path message",
			"agent_config_id", r.agentConfigID, "session_id", sessionID, "error", err)
		return
	}

	// Set status to working before committing
	r.weakUpdateAgentStatusInSession(sessionID, model.ParticipantStatusWorking)

	// Commit the pre-computed message through the serialized channel.
	// This ensures message ordering is preserved even if a normal work
	// is committing at the same time.
	r.draftCommitCh <- &draftCommitRequest{
		draft:     draft,
		sessionID: sessionID,
		content:   payload.ActionContent,
	}

	// Set status back to idle. The commitCh is buffered and handleCommits
	// processes it asynchronously, but the status transition is safe because
	// commitDraft does not modify status — it only updates last_active_at
	// and last_read_message_id. The SSE push from commitDraft will arrive
	// at the client after this status change, which is the correct order.
	r.weakUpdateAgentStatusInSession(sessionID, model.ParticipantStatusIdle)

	applogger.Info("Fast path message dispatched",
		"agent_config_id", r.agentConfigID,
		"session_id", sessionID,
		"draft_id", draft.ID,
		"scheduled_event_id", payload.ScheduledEventID,
	)
}

// ==========================================================================
// Status Management
// ==========================================================================

// weakUpdateAgentStatusInSession updates the agent's ParticipantSession.Status in the database
// and fires the SSE callback if the status actually changed.
func (r *agentRuntime) weakUpdateAgentStatusInSession(sessionID int64, status int) {
	// Read current status from DB to detect changes
	var ps model.ParticipantSession
	err := database.DB.Where(
		"session_id = ? AND participant_id = ?",
		sessionID, r.agentPersonID,
	).First(&ps).Error

	if err != nil {
		applogger.Error("Failed to read participant status",
			"agent_config_id", r.agentConfigID, "session_id", sessionID, "error", err)
		return
	}

	if ps.Status == status {
		return // No change, skip update and callback
	}

	// Persist new status to database
	if err := database.DB.Model(&model.ParticipantSession{}).
		Where("session_id = ? AND participant_id = ?",
			sessionID, r.agentPersonID).
		Update("status", status).Error; err != nil {
		applogger.Error("Failed to update participant status",
			"agent_config_id", r.agentConfigID, "session_id", sessionID, "error", err)
		return
	}

	// Fire SSE callback for status change
	if r.onStatusChange != nil {
		r.onStatusChange(r.agentConfigID, r.agentPersonID, sessionID, status)
	}
}

// hasActiveWorkInSession checks whether any active work exists for the
// given session. Used to determine if the agent can transition to idle
// when a work completes.
func (r *agentRuntime) hasActiveWorkInSession(sessionID int64) bool {
	for _, w := range r.activeWorks {
		if w.sessionID == sessionID {
			return true
		}
	}
	return false
}

// ==========================================================================
// Heartbeat Timer
// ==========================================================================

// resetHeartbeatTimer resets the heartbeat timer with tickless adaptive intervals.
//
// Three-phase model (inspired by Linux NOHZ):
//   - Active (5min): agent just interacted, context is fresh
//   - Steady (30min): session has activity but agent doesn't participate
//   - Dormant (2h): session has been idle for a long time
//
// Events reset idleTicks to 0, naturally returning to the active phase.
// Consecutive "none" self-reflections increment idleTicks, transitioning
// through steady to dormant.
func (r *agentRuntime) resetHeartbeatTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}

	interval := r.adjustHeartbeatInterval()
	timer.Reset(interval)
}

// adjustHeartbeatInterval computes the current heartbeat interval based on
// the idle tick counter. The interval grows as the agent stays idle longer.
func (r *agentRuntime) adjustHeartbeatInterval() time.Duration {
	switch {
	case r.idleTicks == 0:
		return heartbeatActive
	case r.idleTicks <= ticksToSteady:
		return heartbeatActive
	case r.idleTicks <= ticksToDormant:
		return heartbeatSteady
	default:
		return heartbeatDormant
	}
}

// createAgentRuntime creates and initializes an agentRuntime struct without starting
// the event loop. Loads the agent's LLM config, subscribes to the event queue,
// and recovers abandoned works from a previous run.
//
// This is the public entry point for creating a new agent runtime — positioned
// at the bottom because it depends on recoverActiveWorks (defined just above).
func createAgentRuntime(agentConfigID int64, onStatusChange func(agentConfigID, personID, sessionID int64, status int)) (*agentRuntime, error) {
	eventCh := eventqueue.Subscribe(agentConfigID)

	runtime := newAgentRuntime(agentConfigID, eventCh, 30*time.Second, onStatusChange)

	// Resolve agent's PersonID for participant_session queries
	ac, err := dops.Get[model.AgentConfig](agentConfigID)
	if err != nil {
		return nil, fmt.Errorf("createAgentRuntime: failed to load agent config %d: %w", agentConfigID, err)
	}
	runtime.agentPersonID = ac.PersonID

	// Recover any abandoned works from previous run
	recoverActiveWorks(agentConfigID)

	return runtime, nil
}
