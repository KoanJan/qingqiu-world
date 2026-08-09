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
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/agent"
	"qingqiu-world-server/internal/service/chat"
	"qingqiu-world-server/internal/service/comprehend"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/memory"
	"qingqiu-world-server/internal/service/privatespace"
	"qingqiu-world-server/internal/service/task"

	applogger "qingqiu-world-server/internal/logger"
)

// ==========================================================================
// Types & Constants
// ==========================================================================

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
	messageCommitCh    chan *commitRequest
	heartbeatInterval  time.Duration                                              // Base heartbeat interval (adaptive)
	idleTicks          int                                                        // Consecutive idle heartbeats (for tickless backoff)
	heartbeatTick      int                                                        // Total heartbeat ticks (for check scheduling)
	mu                 sync.Mutex                                                 // Protects activeWrites for external queries
	learningInProgress atomic.Bool                                                // Guards against concurrent learning checks
	privateSpaceLoop   *privatespace.Loop                                         // Private-space loop (nil if not initialized)
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
		messageCommitCh:   make(chan *commitRequest, 16),
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

	// Start message commit handler goroutine
	internalWg.Add(1)
	go func() {
		defer internalWg.Done()
		r.handleMessageCommits(ctx)
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

			// Wait for message commit handler to drain its channel
			internalWg.Wait()

			applogger.Info("agentRuntime stopped", "agent_config_id", r.agentConfigID)
			return

		case event := <-r.eventCh:
			if event == nil {
				applogger.Error("agent event channel closed", "agent_config_id", r.agentConfigID)
				return
			}
			// External events reset idleTicks so the adaptive backoff
			// (Active→Steady→Dormant) restarts from heartbeatBase.
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
	if state.Energy < int(energyCost(SituationSourceExternal)) {
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
	a, err := agent.GetAgent(r.agentPersonID)
	if err != nil {
		applogger.Error("handleEvent: failed to load agent", "person_id", r.agentPersonID, "error", err)
		return true
	}
	c := comprehend.Comprehend(ctx, event, &a.Config, &a.LLM, buildActiveWorksSummary(r.activeWorks, event.SessionID))
	situation := buildExternalSituation(event, c, state.Energy, c.ActiveWorksSummary)
	// Do not pass the agent pointer across function boundaries — Decide will
	// fetch its own copy via agent.GetAgent when it needs agent data.
	d := Decide(ctx, situation, r.agentPersonID, r.activeWorks)
	if event.Type == eventqueue.EventTypeNewPrivateChatMessage && c.ReadMessageRange[1] > c.ReadMessageRange[0] {
		if err := dops.AdvanceLastReadMessageID(event.SessionID, r.agentPersonID, c.ReadMessageRange[1]); err != nil {
			applogger.Error("failed to advance last_read_message_id", "session_id", event.SessionID, "person_id", r.agentPersonID, "message_id", c.ReadMessageRange[1], "error", err)
		}
	}
	if len(d.Actions) > 0 {
		if err := energy.DeductEnergy(r.agentPersonID, energyCost(situation.Source)); err != nil {
			applogger.Error("failed to deduct energy", "person_id", r.agentPersonID, "error", err)
		}
	}
	r.executeActions(ctx, situation, d.Actions)
	return true
}

// executeActions dispatches Decide output Actions to their handlers.
// Shared by both external event and internal heartbeat paths.
func (r *agentRuntime) executeActions(ctx context.Context, situation *Situation, actions []action.Action) {
	for _, act := range actions {
		switch act.Type {
		case action.RouteTask, action.CancelTask:
			if act.WorkGuidance == nil {
				applogger.Error("work guidance is missing", "agent_config_id", r.agentConfigID, "action_type", act.Type)
				continue
			}
			target := r.findActiveWorkByID(act.WorkGuidance.TargetWorkID)
			if target == nil {
				applogger.Error("target work not found", "agent_config_id", r.agentConfigID, "work_id", act.WorkGuidance.TargetWorkID)
				continue
			}
			if act.Type == action.CancelTask {
				target.abandon()
				continue
			}
			target.FeedGuidance(task.GuidanceDirective{Guidance: act.WorkGuidance.Guidance, Reason: act.Reason})
		case action.Chat:
			if act.ChatPlan == nil {
				applogger.Error("chat action has no chat plan", "agent_config_id", r.agentConfigID)
				continue
			}
			go r.executeChat(ctx, situation, act.ChatPlan)
		case action.CreateTask:
			if act.WorkPlan == nil {
				applogger.Error("create_task action has no work plan", "agent_config_id", r.agentConfigID)
				continue
			}
			w, success := r.newWork(situation, act)
			if !success {
				applogger.Error("failed to create work", "agent_config_id", r.agentConfigID)
				continue
			}
			if situation.Matter.Event != nil {
				if payload, ok := situation.Matter.Event.Payload.(*eventqueue.WorkCompletedPayload); ok && payload != nil {
					w.taskResult = &task.TaskResult{Status: payload.Status, Output: payload.TaskOutput, Error: payload.TaskError}
				}
			}
			r.activeWorks = append(r.activeWorks, w)
			go w.Run(ctx)
		case action.CreateAlarm:
			if act.AlarmPlan == nil {
				applogger.Error("create_alarm action has no alarm_plan", "agent_config_id", r.agentConfigID)
				continue
			}
			r.handleCreateAlarmAction(act.AlarmPlan, situation)
		case action.UpdateBio:
			if act.BioUpdate == nil {
				applogger.Error("update_bio action has no bio_update", "agent_config_id", r.agentConfigID)
				continue
			}
			if err := dops.UpdateAgentBio(r.agentPersonID, act.BioUpdate.Bio); err != nil {
				applogger.Error("failed to update agent bio", "agent_config_id", r.agentConfigID, "error", err)
			} else {
				agent.Refresh(r.agentPersonID)
			}
		case action.EnterPrivateSpace:
			thoughts := act.Background
			if act.Reason != "" {
				if thoughts != "" {
					thoughts += "\n"
				}
				thoughts += act.Reason
			}
			r.handleEnterPrivateSpace(thoughts)
		}
	}
}

// handleEnterPrivateSpace manages the private-space loop lifecycle.
// If the loop has never been initialized, it lazily creates the directory and loop.
// If the loop is already running, thoughts are injected via channel.
// If the loop is idle, a new goroutine is started.
func (r *agentRuntime) handleEnterPrivateSpace(thoughts string) {
	// Lazy initialization: create the loop on first use.
	if r.privateSpaceLoop == nil {
		a, err := agent.GetAgent(r.agentPersonID)
		if err != nil {
			applogger.Error("private-space: failed to load agent",
				"person_id", r.agentPersonID, "error", err,
			)
			return
		}
		rootDir, workDir, err := privatespace.InitDir(r.agentPersonID)
		if err != nil {
			applogger.Error("private-space: failed to init directory",
				"person_id", r.agentPersonID, "error", err,
			)
			return
		}
		r.privateSpaceLoop = privatespace.NewLoop(
			r.agentPersonID,
			rootDir,
			workDir,
			&a.LLM,
			0, // Use default max iterations
		)
	}

	if r.privateSpaceLoop.IsRunning() {
		r.privateSpaceLoop.FeedThoughts(thoughts)
		applogger.Info("private-space: thoughts injected into running loop",
			"person_id", r.agentPersonID,
		)
	} else {
		// Feed the initial thoughts, then start the loop in a new goroutine.
		r.privateSpaceLoop.FeedThoughts(thoughts)
		go func() {
			ctx := context.Background()
			r.privateSpaceLoop.Run(ctx)
		}()
	}
}

// executeaction.Chat handles a action.Chat action as a lightweight async operation.
// It does not create a Work record — it launches a goroutine that calls
// chat.Executeaction.Chat and commits the result directly via the message commit
// channel.
func (r *agentRuntime) executeChat(ctx context.Context, situation *Situation, plan *action.ChatPlan) {
	if plan.SessionID == 0 {
		applogger.Error("executeChat: session_id is 0 (invalid), skipping",
			"agent_config_id", r.agentConfigID,
		)
		return
	}

	var targetSessionID int64
	event := situation.Matter.Event
	comprehension := situation.Matter.Comprehension

	if plan.UseNewSession() {
		// -1: create a new 1v1 session with RecipientPersonID.
		if plan.RecipientPersonID == r.agentPersonID {
			applogger.Error("executeChat: recipient is self, skipping",
				"agent_config_id", r.agentConfigID,
			)
			return
		}
		newSessionID, err := dops.CreateDirectSession(r.agentPersonID, plan.RecipientPersonID)
		if err != nil {
			applogger.Error("executeChat: failed to create session",
				"agent_config_id", r.agentConfigID,
				"recipient_person_id", plan.RecipientPersonID,
				"error", err,
			)
			return
		}
		targetSessionID = newSessionID
	} else {
		targetSessionID = plan.SessionID
	}

	session, err := dops.GetSession(targetSessionID)
	if err != nil {
		applogger.Error("executeChat: failed to load session",
			"session_id", targetSessionID, "error", err)
		return
	}

	// Build unified Trigger from the event. For normal messages, the trigger
	// type is set to TriggerMessage; loadMessages will fill in the DB message.
	// For scheduled (alarm) events, the trigger carries the self-reminder.
	// For heartbeat (no event), trigger stays nil (TriggerNone).
	var trigger *chat.Trigger
	if event != nil {
		if payload, ok := event.Payload.(*eventqueue.ScheduledEventPayload); ok {
			trigger = &chat.Trigger{
				Type: chat.TriggerAlarm,
				Alarm: &chat.TriggerAlarmData{
					SelfReminder: payload.Message,
				},
			}
		} else {
			trigger = &chat.Trigger{Type: chat.TriggerMessage}
		}
	}

	var chatCtx *chat.ChatContext
	var readMessageRange [2]int64
	if comprehension != nil {
		chatCtx = &chat.ChatContext{
			PersonState:        comprehension.PersonState,
			HistorySegments:    historySegments(comprehension.HistorySearch),
			KBSegments:         kbSegments(comprehension.KBRetrieval),
			NeedsClarification: comprehension.NeedsClarification,
			Clarification:      comprehension.Clarification,
		}
		readMessageRange = comprehension.ReadMessageRange
	}

	result, err := chat.ExecuteChat(
		ctx, session, r.agentPersonID,
		readMessageRange,
		trigger,
		plan.Guidance,
		chatCtx,
	)
	if err != nil {
		if ctx.Err() != nil {
			applogger.Info("executeChat: cancelled", "agent_config_id", r.agentConfigID)
		} else {
			applogger.Error("executeChat: chat execution failed",
				"agent_config_id", r.agentConfigID,
				"session_id", targetSessionID,
				"error", err,
			)
		}
		return
	}

	r.messageCommitCh <- &commitRequest{
		sessionID: targetSessionID,
		content:   result.Content,
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

// newWork creates a new TaskWork from a Situation and anaction.Action, persists it to the
// database, and returns the work object. Only used for CreateTask actions.
func (r *agentRuntime) newWork(situation *Situation, dec action.Action) (*work, bool) {
	plan := dec.WorkPlan
	event := situation.Matter.Event
	comprehension := situation.Matter.Comprehension

	plan.Metadata = buildMetadata(event)

	targetSessionID := int64(0)
	if event != nil {
		targetSessionID = event.SessionID
	}

	tx := database.DB.Begin()
	defer tx.Rollback()

	var workDescription string
	if event != nil {
		workDescription = event.FormatDescription()
	} else {
		workDescription = situation.Matter.Description
	}
	workRecord := &model.Work{
		PersonID:    r.agentPersonID,
		SessionID:   targetSessionID,
		Description: workDescription,
		Status:      model.WorkStatusRunning,
	}
	if err := tx.Create(workRecord).Error; err != nil {
		applogger.Error("Failed to create work", "agent_config_id", r.agentConfigID, "session_id", targetSessionID, "error", err)
		return nil, false
	}

	w := &work{
		ID:            workRecord.ID,
		agent:         r,
		sessionID:     targetSessionID,
		plan:          plan,
		maxIterations: 90,
		comprehension: comprehension,
		guidanceCh:    make(chan task.GuidanceDirective, 8),
		done:          make(chan struct{}),
		triggerAction: &action.Action{
			Background: dec.Background,
			Reason:     dec.Reason,
		},
	}

	if err := tx.Commit().Error; err != nil {
		applogger.Error("Failed to commit work transaction", "agent_config_id", r.agentConfigID, "error", err)
		return nil, false
	}

	applogger.Info("Work created",
		"work_id", w.ID,
		"agent_config_id", r.agentConfigID,
		"session_id", w.sessionID,
	)

	return w, true
}

// buildMetadata constructs system-generated Metadata from the triggering event.
// This is used by the task loop to understand its origin (session, self-reminder, etc.)
// and to power tools like search_chat_histories with the correct session context.
// Returns nil when event is nil (heartbeat-triggered work has no event metadata).
func buildMetadata(event *eventqueue.AgentEvent) *task.Metadata {
	if event == nil {
		return nil
	}
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
		trigger := "a previous work completed"
		if payload, ok := event.Payload.(*eventqueue.WorkCompletedPayload); ok && payload != nil && payload.TriggerAction != nil {
			trigger = payload.TriggerAction.Background
		}
		return &task.Metadata{
			SourceType: task.SourceTypeWorkCompleted,
			SessionMeta: &task.SessionMeta{
				SessionID: event.SessionID,
				Trigger:   trigger,
			},
		}
	}
	return nil
}

// ==========================================================================
// Fast Path
// ==========================================================================

// handleFastPathSendMessage handles the fast path for scheduled events with
// action=send_message. It directly commits a message with pre-computed content
// through the serialized messageCommitCh, skipping the entire LLM pipeline.
// No Work or Draft objects are created.
func (r *agentRuntime) handleFastPathSendMessage(sessionID int64, payload *eventqueue.ScheduledEventPayload) {
	applogger.Info("Fast path: sending pre-computed message for scheduled event",
		"agent_config_id", r.agentConfigID,
		"session_id", sessionID,
		"scheduled_event_id", payload.ScheduledEventID,
	)

	// Set status to working before committing
	r.weakUpdateAgentStatusInSession(sessionID, model.ParticipantStatusWorking)

	// Commit the pre-computed message through the serialized channel.
	r.messageCommitCh <- &commitRequest{
		sessionID: sessionID,
		content:   payload.ActionContent,
	}

	// Set status back to idle after dispatching the commit.
	r.weakUpdateAgentStatusInSession(sessionID, model.ParticipantStatusIdle)

	applogger.Info("Fast path message dispatched",
		"agent_config_id", r.agentConfigID,
		"session_id", sessionID,
		"scheduled_event_id", payload.ScheduledEventID,
	)
}

// alarmTriggerAtFormat is the only accepted time format for AlarmPlan.TriggerAt.
const alarmTriggerAtFormat = "2006-01-02 15:04:05"

// handleCreateAlarmAction executes a action.CreateAlarm action.
func (r *agentRuntime) handleCreateAlarmAction(plan *action.AlarmPlan, situation *Situation) {
	var sessionID int64
	if situation.Matter.Event != nil {
		sessionID = situation.Matter.Event.SessionID
	}
	triggerAt, err := time.ParseInLocation(alarmTriggerAtFormat, plan.TriggerAt, time.Local)
	if err != nil {
		applogger.Error("action.CreateAlarm: invalid trigger_at format, skipping",
			"agent_config_id", r.agentConfigID,
			"trigger_at", plan.TriggerAt,
			"error", err,
		)
		return
	}
	if triggerAt.Before(time.Now()) {
		applogger.Error("action.CreateAlarm: trigger_at is in the past, skipping",
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
		applogger.Error("action.CreateAlarm: 'send_message' action requires action_content, skipping",
			"agent_config_id", r.agentConfigID,
		)
		return
	}

	record := model.ScheduledEvent{
		PersonID:      r.agentPersonID,
		SessionID:     sessionID,
		TriggerAt:     triggerAt,
		Message:       plan.Message,
		Action:        action,
		ActionContent: plan.ActionContent,
		Status:        model.ScheduledEventStatusPending,
	}
	if err := database.DB.Create(&record).Error; err != nil {
		applogger.Error("action.CreateAlarm: failed to create scheduled event record",
			"agent_config_id", r.agentConfigID,
			"person_id", r.agentPersonID,
			"error", err,
		)
		return
	}

	eventqueue.SendEvent(r.agentConfigID, &eventqueue.AgentEvent{
		Type:      eventqueue.EventTypeAlarmCreated,
		SessionID: sessionID,
		Payload: &eventqueue.AlarmCreatedPayload{
			ScheduledEventID: record.ID,
		},
	})

	until := time.Until(triggerAt).Round(time.Minute)
	applogger.Info("action.CreateAlarm: alarm set",
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

	if err := database.DB.Model(&model.ParticipantSession{}).
		Where("session_id = ? AND participant_id = ?",
			sessionID, r.agentPersonID).
		Update("status", status).Error; err != nil {
		applogger.Error("Failed to update participant status",
			"agent_config_id", r.agentConfigID, "session_id", sessionID, "error", err)
		return
	}

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
// double each idle tick, capped at heartbeatMax (6h).
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
func (r *agentRuntime) adjustHeartbeatInterval() time.Duration {
	if r.idleTicks == 0 {
		return heartbeatBase
	}
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

// ==========================================================================
// Runtime Factory
// ==========================================================================

// createAgentRuntime creates and initializes an agentRuntime struct without starting
// the event loop. Loads the agent's LLM config, subscribes to the event queue,
// and recovers abandoned works from a previous run.
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
	// initialized/refreshed before the first event arrives. Non-fatal.
	if _, err := energy.RecoverEnergy(ac.PersonID); err != nil {
		applogger.Error("energy startup recovery failed",
			"agent_config_id", agentConfigID, "person_id", ac.PersonID, "error", err)
	}

	return runtime, nil
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
