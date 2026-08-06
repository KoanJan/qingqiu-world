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
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/memory"
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

// Heartbeat interval constants for exponential backoff.
//
// After an external event, heartbeats back off exponentially starting from
// heartbeatBase (30min). Each subsequent idle heartbeat doubles the interval,
// capped at heartbeatMax (6h):
//
//	tick 1 → 30min, tick 2 → 60min, tick 3 → 120min, tick 4 → 240min,
//	tick 5+ → 360min (capped at heartbeatMax)
//
// Formula: t(n) = min(heartbeatMax, heartbeatBase * 2^(n-1))
// Any external event resets idleTicks to 0, restarting the cycle.
const (
	heartbeatBase = 30 * time.Minute // Base interval (also the first heartbeat after an external event)
	heartbeatMax  = 6 * time.Hour    // Maximum heartbeat interval
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
	r.replayBufferedEvents(ctx)

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
			if event == nil {
				applogger.Error("agent event channel closed", "agent_config_id", r.agentConfigID)
				return
			}
			// Only external events (user messages, A2A messages, alarms, etc.)
			// reset idleTicks — heartbeat events must NOT, otherwise the
			// adaptive backoff (Active→Steady→Dormant) never takes effect.
			if event.Type != eventqueue.EventTypeHeartbeat {
				r.idleTicks = 0
			}
			if sleepSince, err := dops.GetAgentSleepSince(r.agentPersonID); err != nil {
				applogger.Error("failed to read agent sleep state", "person_id", r.agentPersonID, "error", err)
			} else if sleepSince != "" {
				r.replayBufferedEvents(ctx)
			}
			r.handleEvent(ctx, event, false)
			r.resetHeartbeatTimer(heartbeatTimer)

		case <-heartbeatTimer.C:
			r.handleHeartbeat(ctx)
			r.resetHeartbeatTimer(heartbeatTimer)
		}
	}
}

func (r *agentRuntime) handleEvent(ctx context.Context, event *eventqueue.AgentEvent, isReplay bool) bool {
	// Determine the trigger source from the event type. Heartbeat events use
	// the active-behavior path (CostActive); all other events use the passive
	// response path (CostPassive).
	triggerSource := TriggerSourceEvent
	if event.Type == eventqueue.EventTypeHeartbeat {
		triggerSource = TriggerSourceHeartbeat
	}

	state, err := energy.RecoverEnergy(r.agentPersonID)
	if err != nil {
		applogger.Error("energy recovery failed", "error", err)
		return false
	}
	if state.Energy < int(energyCost(triggerSource)) {
		if isReplay {
			applogger.Info("skipped buffered agent event due to insufficient energy",
				"person_id", r.agentPersonID,
				"event_type", event.Type,
				"session_id", event.SessionID,
				"event_id", event.EventID,
				"energy", state.Energy,
			)
			return false
		}
		// Heartbeat events are never buffered — they are transient "time has
		// passed" signals. If the agent lacks Energy for active behavior, it
		// simply does not get an autonomous opportunity this tick. Buffering
		// would create a backlog of stale heartbeat intents.
		if event.Type == eventqueue.EventTypeHeartbeat {
			applogger.Info("skipped heartbeat event due to insufficient energy for active behavior",
				"person_id", r.agentPersonID,
				"energy", state.Energy,
				"required", int(energyCost(triggerSource)),
			)
			return false
		}
		if err := r.bufferEvent(event); err != nil {
			applogger.Error("failed to buffer event", "error", err)
		}
		return false
	}
	if event.Type == eventqueue.EventTypeAlarmCreated {
		if p, ok := event.Payload.(*eventqueue.AlarmCreatedPayload); ok {
			r.handleAlarmCreated(p.ScheduledEventID)
		}
		return true
	}
	if event.Type == eventqueue.EventTypeWorkCompleted {
		payload, ok := event.Payload.(*eventqueue.WorkCompletedPayload)
		if !ok || payload == nil {
			applogger.Error("invalid work completed event payload", "agent_config_id", r.agentConfigID)
			return true
		}
		r.activeWorks = removeWorkByID(r.activeWorks, payload.WorkID)
		if !r.hasActiveWorkInSession(event.SessionID) {
			r.weakUpdateAgentStatusInSession(event.SessionID, model.ParticipantStatusIdle)
		}
	}
	if event.Type == eventqueue.EventTypeNewPrivateChatMessage {
		if event.EventID > 0 {
			if err := memory.CreateObservation(r.agentPersonID, event.EventID); err != nil {
				applogger.Error("failed to create observation", "person_id", r.agentPersonID, "event_id", event.EventID, "error", err)
			}
		}
		p, ok := event.Payload.(*eventqueue.NewMessagePayload)
		if !ok {
			return true
		}
		ps, err := dops.GetParticipantSession(event.SessionID, r.agentPersonID)
		if err != nil {
			applogger.Error("failed to load participant session for message event",
				"session_id", event.SessionID,
				"person_id", r.agentPersonID,
				"message_id", p.MessageID,
				"error", err,
			)
			return true
		}
		if ps.LastReadMessageID >= p.MessageID {
			applogger.Info("skipped message event because it is already read",
				"session_id", event.SessionID,
				"person_id", r.agentPersonID,
				"message_id", p.MessageID,
				"last_read_message_id", ps.LastReadMessageID,
				"event_id", event.EventID,
			)
			return true
		}
	}
	if event.Type == eventqueue.EventTypeScheduled {
		if p, ok := event.Payload.(*eventqueue.ScheduledEventPayload); ok && p.Action == model.ScheduledEventActionSendMessage && p.ActionContent != "" {
			r.handleFastPathSendMessage(event.SessionID, p)
			return true
		}
	}
	ac, err := dops.Get[model.AgentConfig](r.agentConfigID)
	if err != nil {
		return true
	}
	llmConfig, err := dops.GetLLMConfig(ac.LLMConfigID)
	if err != nil {
		return true
	}
	c := comprehend.Comprehend(ctx, event, ac, llmConfig, buildActiveWorksSummary(r.activeWorks, event.SessionID))
	d := Decide(ctx, event, ac, llmConfig, c, r.activeWorks, triggerSource, state)
	if event.Type == eventqueue.EventTypeNewPrivateChatMessage && c.ReadMessageRange[1] > c.ReadMessageRange[0] {
		if err := dops.AdvanceLastReadMessageID(event.SessionID, r.agentPersonID, c.ReadMessageRange[1]); err != nil {
			applogger.Error("failed to advance last_read_message_id", "session_id", event.SessionID, "person_id", r.agentPersonID, "message_id", c.ReadMessageRange[1], "error", err)
		}
	}
	if len(d.Actions) > 0 {
		if err := energy.DeductEnergy(r.agentPersonID, energyCost(triggerSource)); err != nil {
			applogger.Error("failed to deduct energy", "person_id", r.agentPersonID, "error", err)
		}
	}
	for _, action := range d.Actions {
		switch action.Type {
		case ActionRoute, ActionCancel:
			if action.WorkGuidance == nil {
				applogger.Error("work guidance is missing", "agent_config_id", r.agentConfigID, "action_type", action.Type)
				continue
			}
			target := r.findActiveWorkByID(action.WorkGuidance.TargetWorkID)
			if target == nil {
				applogger.Error("target work not found", "agent_config_id", r.agentConfigID, "work_id", action.WorkGuidance.TargetWorkID)
				continue
			}
			if action.Type == ActionCancel && target.plan.Type != model.WorkTypeTask {
				target.abandon()
				continue
			}
			if target.plan.Type != model.WorkTypeTask {
				applogger.Error("route target is not task work", "agent_config_id", r.agentConfigID, "work_id", target.ID)
				continue
			}
			target.FeedGuidance(task.GuidanceDirective{Guidance: action.WorkGuidance.Guidance, Reason: action.WorkGuidance.Reason})
		case ActionCreate:
			if action.WorkPlan == nil {
				applogger.Error("create action has no work plan", "agent_config_id", r.agentConfigID)
				continue
			}
			w, success := r.newWork(event, action.WorkPlan, c)
			if !success {
				applogger.Error("failed to create work", "agent_config_id", r.agentConfigID)
				continue
			}
			if payload, ok := event.Payload.(*eventqueue.WorkCompletedPayload); ok && payload != nil {
				w.taskResult = &task.TaskResult{Status: payload.Status, Output: payload.TaskOutput, Error: payload.TaskError}
			}
			r.activeWorks = append(r.activeWorks, w)
			go w.Run(ctx)
		case ActionCreateAlarm:
			// CreateAlarm is a top-level world action (0.1.3). It does not
			// enter TaskLoop — setting an alarm is a one-step action: create
			// a ScheduledEvent record and notify the runtime to register a
			// waiting goroutine.
			if action.AlarmPlan == nil {
				applogger.Error("create_alarm action has no alarm_plan", "agent_config_id", r.agentConfigID)
				continue
			}
			r.handleCreateAlarmAction(action.AlarmPlan, event)
		}
	}
	return true
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

	// Resolve the target session for chat works based on delivery_target.
	// For task works, always use the event's session — tasks don't have
	// delivery_target semantics and always execute in the triggering session.
	//
	// delivery_target controls where a ComposeMessageWork delivers its message:
	//   - "" / "reply": respond in the current event's session (event.SessionID)
	//   - "send_to_session": send to an existing session (plan.SessionID)
	//   - "create_and_send": create a new 1v1 session with plan.RecipientPersonID
	//
	// For heartbeat-triggered works, event.SessionID is 0, so "reply" is not
	// valid (validated by isValidCreateAction). The agent must choose
	// "send_to_session" or "create_and_send".
	//
	// createdSessionID tracks a session newly created via create_and_send.
	// If the subsequent transaction (draft + work record) fails, this session
	// must be cleaned up to avoid orphaned empty sessions. The persistence
	// boundary is: either the session AND its first work exist, or neither.
	targetSessionID := event.SessionID
	var createdSessionID int64 // 0 if no new session was created
	if plan.Type == model.WorkTypeChat {
		switch plan.DeliveryTarget {
		case "send_to_session":
			// Validate the agent is actually a participant in the target
			// session. The LLM could hallucinate a session_id it saw in the
			// context list but does not belong to. Sending to a session the
			// agent is not part of would violate the relationship boundary.
			ok, err := dops.IsParticipant(plan.SessionID, r.agentPersonID)
			if err != nil {
				applogger.Error("send_to_session: failed to verify participation",
					"agent_config_id", r.agentConfigID,
					"session_id", plan.SessionID,
					"error", err,
				)
				return nil, false
			}
			if !ok {
				applogger.Error("send_to_session: agent is not a participant in target session, skipping",
					"agent_config_id", r.agentConfigID,
					"session_id", plan.SessionID,
					"person_id", r.agentPersonID,
				)
				return nil, false
			}
			targetSessionID = plan.SessionID
		case "create_and_send":
			// Validate the recipient exists and is not the agent itself.
			// The LLM's contactable-persons list already excludes self, but
			// LLMs can hallucinate — application-layer validation is required.
			if plan.RecipientPersonID == r.agentPersonID {
				applogger.Error("create_and_send: recipient is self, skipping",
					"agent_config_id", r.agentConfigID,
					"person_id", r.agentPersonID,
				)
				return nil, false
			}
			if _, err := dops.Get[model.Person](plan.RecipientPersonID); err != nil {
				applogger.Error("create_and_send: recipient person does not exist, skipping",
					"agent_config_id", r.agentConfigID,
					"recipient_person_id", plan.RecipientPersonID,
					"error", err,
				)
				return nil, false
			}
			newSessionID, err := dops.CreateDirectSession(r.agentPersonID, plan.RecipientPersonID)
			if err != nil {
				applogger.Error("Failed to create direct session for create_and_send",
					"agent_config_id", r.agentConfigID,
					"recipient_person_id", plan.RecipientPersonID,
					"error", err,
				)
				return nil, false
			}
			targetSessionID = newSessionID
			createdSessionID = newSessionID
			applogger.Info("Created new session for create_and_send",
				"agent_config_id", r.agentConfigID,
				"session_id", targetSessionID,
				"recipient_person_id", plan.RecipientPersonID,
			)
		case "", "reply":
			// Use event.SessionID (already set as targetSessionID default)
		default:
			applogger.Error("Unknown delivery_target, falling back to event session",
				"agent_config_id", r.agentConfigID,
				"delivery_target", plan.DeliveryTarget,
			)
		}
	}

	// Cross-session chat works (send_to_session / create_and_send) target a
	// different session than the triggering event. The comprehension carried
	// into this work was computed against the EVENT's session — its
	// ReadMessageRange, PersonState, and history all refer to that session's
	// partner and messages. Carrying them into a different session would
	// contaminate the target session's context: replying to a message that
	// doesn't exist there, describing a partner who isn't in it, and labeling
	// the dialog with the wrong roles. Retain only session-independent
	// self-awareness (ActiveWorksSummary); the plan's guidance + background
	// drive the first message, and the target session's own history is loaded
	// fresh by ExecuteChat (ReadMessageRange[1] == 0 loads all of it).
	if plan.Type == model.WorkTypeChat && targetSessionID != event.SessionID && comprehension != nil {
		comprehension = &comprehend.ComprehensionResult{
			ActiveWorksSummary: comprehension.ActiveWorksSummary,
		}
	}

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

	// If the transaction fails after a new session was created (create_and_send),
	// clean up the orphaned session to maintain the persistence boundary:
	// either the session AND its first work exist, or neither.
	workCreated := false
	defer func() {
		if !workCreated && createdSessionID != 0 {
			r.cleanupOrphanedSession(createdSessionID)
		}
	}()

	if plan.Type == model.WorkTypeChat {
		// create MessageDraft only creating chat work
		if err := tx.Where("session_id = ? AND participant_id = ?",
			targetSessionID, r.agentPersonID).First(ps).Error; err == nil {
			agentLastReadID = ps.LastReadMessageID
		}

		draft = &model.MessageDraft{
			PersonID:          r.agentPersonID,
			SessionID:         targetSessionID,
			Status:            model.DraftStatusBuilding,
			LastReadMessageID: agentLastReadID,
		}
		if err := tx.Create(draft).Error; err != nil {
			applogger.Error("Failed to create draft", "agent_config_id", r.agentConfigID, "session_id", targetSessionID, "error", err)
			return nil, false
		}
	}

	// Persist work to database
	workRecord := &model.Work{
		PersonID:    r.agentPersonID,
		SessionID:   targetSessionID,
		Type:        plan.Type,
		Description: event.FormatDescription(),
		Status:      model.WorkStatusRunning,
	}
	if plan.Type == model.WorkTypeChat {
		workRecord.DraftID = draft.ID
	}
	if err := tx.Create(workRecord).Error; err != nil {
		applogger.Error("Failed to create work", "agent_config_id", r.agentConfigID, "session_id", targetSessionID, "error", err)
		return nil, false
	}

	w := &work{
		ID:             workRecord.ID,
		agent:          r,
		sessionID:      targetSessionID,
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
		applogger.Error("Failed to create work", "agent_config_id", r.agentConfigID, "session_id", targetSessionID, "error", err)
		return nil, false
	}

	workCreated = true
	r.weakUpdateAgentStatusInSession(targetSessionID, model.ParticipantStatusWorking)
	return w, true
}

// cleanupOrphanedSession deletes a session that was created for create_and_send
// but whose subsequent work creation failed. This maintains the persistence
// boundary: an empty session with no work and no messages should not persist.
// Best-effort — if cleanup itself fails, the error is logged but not propagated
// (the caller is already on an error path).
func (r *agentRuntime) cleanupOrphanedSession(sessionID int64) {
	if err := database.DB.Where("session_id = ?", sessionID).
		Delete(&model.ParticipantSession{}).Error; err != nil {
		applogger.Error("cleanupOrphanedSession: failed to delete participant_sessions",
			"session_id", sessionID, "error", err)
	}
	if err := database.DB.Delete(&model.Session{}, sessionID).Error; err != nil {
		applogger.Error("cleanupOrphanedSession: failed to delete session",
			"session_id", sessionID, "error", err)
		return
	}
	applogger.Info("Cleaned up orphaned session after work creation failure",
		"agent_config_id", r.agentConfigID,
		"session_id", sessionID,
	)
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
					SessionID:  event.SessionID,
					Trigger:    fmt.Sprintf("%s sent a chat message: %q", payload.SpeakerName, payload.MessageContent),
					SenderName: payload.SpeakerName,
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
				Trigger:   "a previous work completed",
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

// alarmTriggerAtFormat is the only accepted time format for AlarmPlan.TriggerAt.
// Uses server local time without timezone — the agent and server share the
// same timezone context.
const alarmTriggerAtFormat = "2006-01-02 15:04:05"

// handleCreateAlarmAction executes a CreateAlarm action (0.1.3).
//
// This is the top-level Action form of the former wake_me_when tool — setting
// an alarm is a world action, not a workspace operation. The logic mirrors the
// tool exactly: create a ScheduledEvent DB record (status=Pending), then send
// an EventTypeAlarmCreated event so the runtime registers a waiting goroutine.
//
// The session_id of the ScheduledEvent is set to the triggering event's
// SessionID when available (e.g., a private chat message), or 0 when the
// action was produced from a heartbeat (no specific session context). This
// matches the spec: "ScheduledEvent 的 session_id 字段在 Action 路径下可为空
// 或指向 Agent 的某个已有会话，不强制绑定到触发 TaskLoop 的 session."
func (r *agentRuntime) handleCreateAlarmAction(plan *AlarmPlan, event *eventqueue.AgentEvent) {
	triggerAt, err := time.ParseInLocation(alarmTriggerAtFormat, plan.TriggerAt, time.Local)
	if err != nil {
		applogger.Error("CreateAlarm: invalid trigger_at format, skipping",
			"agent_config_id", r.agentConfigID,
			"trigger_at", plan.TriggerAt,
			"error", err,
		)
		return
	}
	if triggerAt.Before(time.Now()) {
		applogger.Error("CreateAlarm: trigger_at is in the past, skipping",
			"agent_config_id", r.agentConfigID,
			"trigger_at", plan.TriggerAt,
		)
		return
	}

	action := model.ScheduledEventActionFullPipeline
	if plan.Action == "send_message" {
		action = model.ScheduledEventActionSendMessage
	}
	if action == model.ScheduledEventActionSendMessage && plan.ActionContent == "" {
		applogger.Error("CreateAlarm: 'send_message' action requires action_content, skipping",
			"agent_config_id", r.agentConfigID,
		)
		return
	}

	record := model.ScheduledEvent{
		PersonID:      r.agentPersonID,
		SessionID:     event.SessionID, // 0 for heartbeat-triggered alarms
		TriggerAt:     triggerAt,
		Message:       plan.Message,
		Action:        action,
		ActionContent: plan.ActionContent,
		Status:        model.ScheduledEventStatusPending,
	}
	if err := database.DB.Create(&record).Error; err != nil {
		applogger.Error("CreateAlarm: failed to create scheduled event record",
			"agent_config_id", r.agentConfigID,
			"person_id", r.agentPersonID,
			"error", err,
		)
		return
	}

	eventqueue.SendEvent(r.agentConfigID, &eventqueue.AgentEvent{
		Type:      eventqueue.EventTypeAlarmCreated,
		SessionID: event.SessionID,
		Payload: &eventqueue.AlarmCreatedPayload{
			ScheduledEventID: record.ID,
		},
	})

	until := time.Until(triggerAt).Round(time.Minute)
	applogger.Info("CreateAlarm: alarm set",
		"agent_config_id", r.agentConfigID,
		"person_id", r.agentPersonID,
		"scheduled_event_id", record.ID,
		"trigger_at", triggerAt.Format("2006-01-02 15:04 MST"),
		"in", until,
		"action", action,
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

// resetHeartbeatTimer resets the heartbeat timer with exponential backoff.
//
// Heartbeats start at heartbeatBase (30min) after any external event, then
// double each idle tick, capped at heartbeatMax (6h):
//
//	tick 1 → 30min, tick 2 → 60min, tick 3 → 120min, tick 4 → 240min, ...
//
// Any external event (user message, A2A message, alarm) resets idleTicks
// to 0, restarting the cycle from heartbeatBase.
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

// adjustHeartbeatInterval computes the current heartbeat interval using
// exponential backoff: t(n) = min(heartbeatMax, heartbeatBase * 2^(n-1)).
//
// idleTicks == 0 means an external event just occurred — use heartbeatBase.
// idleTicks >= 1 means the agent has been idle and chose "do nothing" —
// back off exponentially.
func (r *agentRuntime) adjustHeartbeatInterval() time.Duration {
	if r.idleTicks == 0 {
		return heartbeatBase
	}
	// Exponential backoff: 30min, 60min, 120min, 240min, 360min (capped).
	// Cap the shift to avoid overflow for very large idleTicks values;
	// 30min << 8 = 128h >> 6h, so anything beyond 8 is already capped.
	shift := r.idleTicks - 1
	if shift > 8 {
		shift = 8
	}
	interval := heartbeatBase * time.Duration(1<<shift)
	if interval > heartbeatMax {
		return heartbeatMax
	}
	return interval
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

	// Energy: trigger lazy recovery on startup so the agent's energy state is
	// initialized/refreshed before the first event arrives. Non-fatal — the
	// event loop retries RecoverEnergy on the first event if this fails.
	if _, err := energy.RecoverEnergy(ac.PersonID); err != nil {
		applogger.Error("energy startup recovery failed",
			"agent_config_id", agentConfigID, "person_id", ac.PersonID, "error", err)
	}

	return runtime, nil
}
