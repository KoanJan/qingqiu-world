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
			r.idleTicks = 0
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
	state, err := energy.RecoverEnergy(r.agentPersonID)
	if err != nil {
		applogger.Error("energy recovery failed", "error", err)
		return false
	}
	if state.Energy < int(energyCost(TriggerSourceEvent)) {
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
	d := Decide(ctx, event, ac, llmConfig, c, r.activeWorks, TriggerSourceEvent, state)
	if event.Type == eventqueue.EventTypeNewPrivateChatMessage && c.ReadMessageRange[1] > c.ReadMessageRange[0] {
		if err := dops.AdvanceLastReadMessageID(event.SessionID, r.agentPersonID, c.ReadMessageRange[1]); err != nil {
			applogger.Error("failed to advance last_read_message_id", "session_id", event.SessionID, "person_id", r.agentPersonID, "message_id", c.ReadMessageRange[1], "error", err)
		}
	}
	if len(d.Actions) > 0 {
		if err := energy.DeductEnergy(r.agentPersonID, energyCost(TriggerSourceEvent)); err != nil {
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

	// Energy: trigger lazy recovery on startup so the agent's energy state is
	// initialized/refreshed before the first event arrives. Non-fatal — the
	// event loop retries RecoverEnergy on the first event if this fails.
	if _, err := energy.RecoverEnergy(ac.PersonID); err != nil {
		applogger.Error("energy startup recovery failed",
			"agent_config_id", agentConfigID, "person_id", ac.PersonID, "error", err)
	}

	return runtime, nil
}
