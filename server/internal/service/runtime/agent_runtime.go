package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/notification"
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/agent"
	"qingqiu-world-server/internal/service/chat"
	"qingqiu-world-server/internal/service/comprehend"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/focusedwork"
	"qingqiu-world-server/internal/service/jinshu"
	"qingqiu-world-server/internal/service/privatespace"
	"qingqiu-world-server/internal/service/workspace"

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
	heartbeatInterval  time.Duration      // Base heartbeat interval (adaptive)
	idleTicks          int                // Consecutive idle heartbeats (for tickless backoff)
	heartbeatTick      int                // Total heartbeat ticks (for check scheduling)
	mu                 sync.Mutex         // Protects activeWrites for external queries
	learningInProgress atomic.Bool        // Guards against concurrent learning checks
	privateSpaceLoop   *privatespace.Loop // Private-space loop (nil if not initialized)
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
) *agentRuntime {
	return &agentRuntime{
		agentConfigID:     agentConfigID,
		eventCh:           eventCh,
		messageCommitCh:   make(chan *commitRequest, 16),
		heartbeatInterval: heartbeatInterval,
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

	// Wait for the serial message commit worker to drain during shutdown.
	// Active Works have their own completion channels below.
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
			// A shutdown can cancel Comprehend or Decide after this event was
			// removed from the channel. Preserve an undecided event for startup
			// replay instead of losing its only queue delivery.
			if ctx.Err() != nil {
				if err := r.bufferInterruptedEvent(event); err != nil {
					applogger.Error("failed to buffer shutdown-interrupted event", "person_id", r.agentPersonID, "event_id", event.EventID, "error", err)
				}
			}
			r.resetHeartbeatTimer(heartbeatTimer)

		case <-heartbeatTimer.C:
			r.handleHeartbeat(ctx)
			r.resetHeartbeatTimer(heartbeatTimer)
		}
	}
}

func (r *agentRuntime) handleEvent(ctx context.Context, event *eventqueue.AgentEvent, isReplay bool) bool {
	if event == nil {
		applogger.Error("handleEvent: nil event")
		return true
	}
	if event.Type == eventqueue.EventTypeAlarmCreated {
		// Alarm registration is a control-plane effect, not a Decide opportunity.
		if p, ok := event.Payload.(*eventqueue.AlarmCreatedPayload); ok && p != nil {
			armScheduledEvent(p.ScheduledEventID)
		} else {
			applogger.Error("alarm registration has invalid payload", "person_id", r.agentPersonID)
		}
		return true
	}
	if event.EventID <= 0 {
		applogger.Error("external Decide event has no durable Event ID; refusing decision",
			"person_id", r.agentPersonID, "event_type", event.Type)
		return true
	}
	alreadyDecided, err := hasAcceptedDecision(r.agentPersonID, event.EventID)
	if err != nil {
		applogger.Error("failed to check prior decision", "person_id", r.agentPersonID,
			"event_id", event.EventID, "error", err)
		return false
	}
	if alreadyDecided {
		applogger.Info("skipped already-decided Event", "person_id", r.agentPersonID, "event_id", event.EventID)
		return true
	}
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
	// Breathing light: from here on the agent is digesting this session's
	// event (comprehend + decide). Set working at the main-segment entry and
	// always reset to idle on exit — the defer structurally covers every
	// early-return below (agent load failure, comprehension failure, etc.)
	// so no failure path can leave a ghost working lamp. Events outside any
	// session (SessionID == 0, e.g. PS digest) never light the lamp.
	if event.SessionID != 0 {
		r.weakUpdateAgentStatusInSession(event.SessionID, model.ParticipantStatusWorking)
		defer r.weakUpdateAgentStatusInSession(event.SessionID, model.ParticipantStatusIdle)
	}
	a, err := agent.GetAgent(r.agentPersonID)
	if err != nil {
		applogger.Error("handleEvent: failed to load agent", "person_id", r.agentPersonID, "error", err)
		return true
	}
	situation := buildExternalSituation(event, nil, state.Energy, "")
	populateGeneralSituation(r.agentPersonID, situation)
	c, err := comprehend.Comprehend(ctx, event, &a.Config, &a.LLM, situation.Subject.ActiveWorksSummary)
	if err != nil {
		applogger.Error("handleEvent: comprehension failed", "person_id", r.agentPersonID, "error", err)
		return true
	}
	if err := recordComprehendedObservations(r.agentPersonID, event, c); err != nil {
		applogger.Error("handleEvent: failed to record comprehended events", "person_id", r.agentPersonID, "event_id", event.EventID, "error", err)
		return true
	}
	situation.Matter.Comprehension = c
	// Do not pass the agent pointer across function boundaries — Decide will
	// fetch its own copy via agent.GetAgent when it needs agent data.
	d := Decide(ctx, situation, r.agentPersonID, r.activeWorks)
	if !d.Accepted {
		if ctx.Err() != nil {
			applogger.Info("handleEvent: Decide interrupted by shutdown", "person_id", r.agentPersonID, "event_id", event.EventID)
			return true
		}
		applogger.Error("handleEvent: Decide produced no accepted result", "person_id", r.agentPersonID, "event_id", event.EventID)
		return true
	}
	accepted, err := persistDecision(r.agentPersonID, situation, &d)
	if err != nil {
		applogger.Error("handleEvent: failed to persist decision", "person_id", r.agentPersonID, "event_id", event.EventID, "error", err)
		return true
	}
	if !accepted {
		applogger.Info("handleEvent: Event already decided", "person_id", r.agentPersonID, "event_id", event.EventID)
		return true
	}
	if event.Type == eventqueue.EventTypeNewJinshuReceived {
		// The recipient has finished Decide, so the jinshu is now cognitively
		// processed. Mark it read (receiver-only flag).
		if p, ok := event.Payload.(*eventqueue.JinshuReceivedPayload); ok && p != nil {
			if err := dops.MarkJinshuRead(p.JinshuID); err != nil {
				applogger.Error("failed to mark jinshu read", "jinshu_id", p.JinshuID, "person_id", r.agentPersonID, "error", err)
			}
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
		case action.RouteFocusedWork, action.CancelFocusedWork:
			if act.WorkGuidance == nil {
				applogger.Error("work guidance is missing", "agent_config_id", r.agentConfigID, "action_type", act.Type)
				endActionLogged(act.ID, "route_or_cancel")
				continue
			}
			target := r.findActiveWorkByID(act.WorkGuidance.TargetWorkID)
			if target == nil {
				applogger.Error("target work not found", "agent_config_id", r.agentConfigID, "work_id", act.WorkGuidance.TargetWorkID)
				endActionLogged(act.ID, "route_or_cancel")
				continue
			}
			if act.Type == action.CancelFocusedWork {
				if target.requestCancel(act) {
					// A cancelled Work is no longer available for routing or for
					// the active roster in a Chat from this same Decision.
					r.activeWorks = removeWorkByID(r.activeWorks, target.ID)
				}
				endActionLogged(act.ID, "cancel_work")
				continue
			}
			target.FeedGuidance(focusedwork.GuidanceDirective{Guidance: act.WorkGuidance.Guidance, Reason: act.Reason})
			endActionLogged(act.ID, "route_work")
		case action.Chat:
			if act.ChatPlan == nil {
				applogger.Error("chat action has no chat plan", "agent_config_id", r.agentConfigID)
				endActionLogged(act.ID, "chat")
				continue
			}
			activeSnapshot := append([]*work(nil), r.activeWorks...)
			go r.executeChat(ctx, situation, act.ChatPlan, act.ID, activeSnapshot)
		case action.StartFocusedWork:
			if act.WorkPlan == nil {
				applogger.Error("start_focused_work action has no work plan", "agent_config_id", r.agentConfigID)
				endActionLogged(act.ID, "start_work")
				continue
			}
			w, success := r.newWork(situation, act)
			if !success {
				applogger.Error("failed to create work", "agent_config_id", r.agentConfigID)
				endActionLogged(act.ID, "start_work")
				continue
			}
			if situation.Matter.Event != nil {
				if payload, ok := situation.Matter.Event.Payload.(*eventqueue.WorkCompletedPayload); ok && payload != nil {
					w.focusedWorkResult = &focusedwork.FocusedWorkResult{Status: payload.Status, Output: payload.WorkOutput, Error: payload.WorkError}
				}
			}
			r.activeWorks = append(r.activeWorks, w)
			go w.Run(ctx)
		case action.CreateAlarm:
			if act.AlarmPlan == nil {
				applogger.Error("create_alarm action has no alarm_plan", "agent_config_id", r.agentConfigID)
				endActionLogged(act.ID, "create_alarm")
				continue
			}
			r.handleCreateAlarmAction(act, situation)
			endActionLogged(act.ID, "create_alarm")
		case action.UpdateBio:
			if act.BioUpdate == nil {
				applogger.Error("update_bio action has no bio_update", "agent_config_id", r.agentConfigID)
				endActionLogged(act.ID, "update_bio")
				continue
			}
			if err := dops.UpdateAgentBio(r.agentPersonID, act.BioUpdate.Bio); err != nil {
				applogger.Error("failed to update agent bio", "agent_config_id", r.agentConfigID, "error", err)
			} else {
				agent.Refresh(r.agentPersonID)
			}
			endActionLogged(act.ID, "update_bio")
		case action.EnterPrivateSpace:
			thoughts := act.Background
			if act.Reason != "" {
				if thoughts != "" {
					thoughts += "\n"
				}
				thoughts += act.Reason
			}
			r.handleEnterPrivateSpace(thoughts)
			endActionLogged(act.ID, "enter_private_space")
		case action.InspectJinshu:
			if act.JinshuPlan == nil {
				applogger.Error("inspect_jinshu action has no jinshu plan", "agent_config_id", r.agentConfigID)
				endActionLogged(act.ID, "inspect_jinshu")
				continue
			}
			go r.handleInspectJinshu(act)
		case action.ListReceivedJinshu:
			if act.ListReceivedJinshuParams == nil {
				applogger.Error("list_received_jinshu action has no list_received_jinshu_params", "agent_config_id", r.agentConfigID)
				endActionLogged(act.ID, "list_received_jinshu")
				continue
			}
			go r.handleListReceivedJinshu(act)
		case action.SendJinshu:
			if act.SendJinshuPlan == nil {
				applogger.Error("send_jinshu action has no send_jinshu plan", "agent_config_id", r.agentConfigID)
				endActionLogged(act.ID, "send_jinshu")
				continue
			}
			go r.handleSendJinshu(act)
		case action.ListSentJinshu:
			if act.ListSentJinshuParams == nil {
				applogger.Error("list_sent_jinshu action has no list_sent_jinshu_params", "agent_config_id", r.agentConfigID)
				endActionLogged(act.ID, "list_sent_jinshu")
				continue
			}
			go r.handleListSentJinshu(act)
		case action.InspectOwnedSpace:
			if act.OwnedSpaceInspectionPlan == nil {
				applogger.Error("inspect_owned_space: missing plan", "agent_config_id", r.agentConfigID)
				endActionLogged(act.ID, "inspect_owned_space")
				continue
			}
			r.handleInspectOwnedSpace(situation, act)
		default:
			applogger.Error("executeActions: unsupported action type", "action_id", act.ID, "action_type", act.Type)
			endActionLogged(act.ID, "unsupported_action")
		}
	}
}

func (r *agentRuntime) handleInspectOwnedSpace(situation *Situation, act action.Action) {
	plan := act.OwnedSpaceInspectionPlan
	scope := plan.Scope
	entries, err := workspace.InspectOwnedSpace(r.agentPersonID, scope, plan.Query, plan.Limit)
	if err != nil {
		applogger.Error("inspect_owned_space failed", "agent_config_id", r.agentConfigID, "error", err)
		endActionLogged(act.ID, "inspect_owned_space")
		return
	}
	var lines []string
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf("- %s (%s, %d bytes, %s)", entry.Path, entry.Type, entry.Size, entry.Modified))
	}
	result := "no matching resources"
	if len(lines) > 0 {
		result = strings.Join(lines, "\n")
	}
	sessionID := int64(0)
	if situation != nil && situation.Matter.Event != nil {
		sessionID = situation.Matter.Event.SessionID
	}
	payload := &eventqueue.OwnedSpaceInspectedPayload{Scope: scope, Result: result}
	if err := r.emitSelfHeldResult(eventqueue.EventTypeOwnedSpaceInspected, model.EventTypeOwnedSpaceInspected, sessionID, payload, act.ID,
		&eventqueue.TriggerAction{Background: act.Background, Reason: act.Reason}); err != nil {
		applogger.Error("inspect_owned_space: failed to persist result", "action_id", act.ID, "error", err)
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
		r.privateSpaceLoop.SetFocusContext(buildAgentFocusContext(r.agentPersonID, r.activeWorks))
		// Feed the initial thoughts, then start the loop in a new goroutine.
		r.privateSpaceLoop.FeedThoughts(thoughts)
		go func() {
			ctx := context.Background()
			r.privateSpaceLoop.Run(ctx)
		}()
	}
}

// handleInspectJinshu runs the dedicated jinshu-read loop and reflows the
// result back to the agent as a JinshuReadCompleted event, so the agent can
// decide how to react to the contents it just read.
func (r *agentRuntime) handleInspectJinshu(act action.Action) {
	defer endActionLogged(act.ID, "inspect_jinshu")
	plan := act.JinshuPlan
	record, err := jinshu.GetReceived(r.agentPersonID, plan.JinshuID)
	if err != nil {
		applogger.Error("inspect_jinshu: failed to load received jinshu",
			"person_id", r.agentPersonID, "jinshu_id", plan.JinshuID, "error", err)
		r.sendJinshuReadCompleted(act, plan.JinshuID, "", "", "", err)
		return
	}

	fromName := ""
	if from, err := dops.GetPerson(record.FromPersonID); err != nil {
		applogger.Error("inspect_jinshu: failed to load sender",
			"person_id", record.FromPersonID, "error", err)
	} else {
		fromName = from.Name
	}

	entries, err := jinshu.ListReceivedFiles(r.agentPersonID, plan.JinshuID)
	if err != nil {
		applogger.Error("inspect_jinshu: failed to list received files",
			"person_id", r.agentPersonID, "jinshu_id", plan.JinshuID, "error", err)
	}

	a, err := agent.GetAgent(r.agentPersonID)
	if err != nil {
		applogger.Error("inspect_jinshu: failed to load agent",
			"person_id", r.agentPersonID, "error", err)
		r.sendJinshuReadCompleted(act, plan.JinshuID, fromName, record.Topic, "", err)
		return
	}

	loop := jinshu.NewReadLoop(jinshu.ReadConfig{
		PersonID:      r.agentPersonID,
		ReceivedDir:   jinshu.ReceivedDirFor(r.agentPersonID, plan.JinshuID),
		FileList:      formatJinshuFileEntries(entries),
		LLMConfig:     &a.LLM,
		JinshuID:      plan.JinshuID,
		Guidance:      plan.Guidance,
		MaxIterations: 0, // Use default
	})

	summary, err := loop.Run(context.Background())
	if err != nil {
		applogger.Error("inspect_jinshu: read loop failed",
			"person_id", r.agentPersonID, "jinshu_id", plan.JinshuID, "error", err)
		r.sendJinshuReadCompleted(act, plan.JinshuID, fromName, record.Topic, "", err)
		return
	}

	r.sendJinshuReadCompleted(act, plan.JinshuID, fromName, record.Topic, summary, nil)
}

// handleListReceivedJinshu runs the paginated keyword search over the agent's received
// jinshu and reflows the result back as a JinshuListed event so the agent can
// pick a jinshu_id to inspect.
func (r *agentRuntime) handleListReceivedJinshu(act action.Action) {
	defer endActionLogged(act.ID, "list_received_jinshu")
	params := act.ListReceivedJinshuParams

	page := params.Page
	if page < 1 {
		page = 1
	}
	limit := params.Limit
	if limit < 1 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	records, err := dops.SearchReceivedJinshu(r.agentPersonID, params.Query, (page-1)*limit, limit)
	if err != nil {
		applogger.Error("list_received_jinshu: search failed",
			"person_id", r.agentPersonID, "query", params.Query, "error", err)
		r.sendJinshuListed(act, params.Query, page, nil)
		return
	}

	// Resolve sender names in one batch.
	idSet := make(map[int64]struct{})
	for _, rec := range records {
		idSet[rec.FromPersonID] = struct{}{}
	}
	ids := make([]int64, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	names, err := dops.GetPersonNames(ids)
	if err != nil {
		applogger.Error("list_received_jinshu: failed to resolve sender names", "error", err)
		names = map[int64]string{}
	}

	items := make([]eventqueue.JinshuListItem, 0, len(records))
	for _, rec := range records {
		fromName := names[rec.FromPersonID]
		if fromName == "" {
			fromName = fmt.Sprintf("person_%d", rec.FromPersonID)
		}
		items = append(items, eventqueue.JinshuListItem{
			JinshuID:  rec.ID,
			FromName:  fromName,
			Topic:     rec.Topic,
			IsRead:    rec.IsRead,
			CreatedAt: rec.CreatedAt.Format("2006-01-02 15:04"),
		})
	}

	r.sendJinshuListed(act, params.Query, page, items)
}

// sendJinshuListed dispatches the list result back to the agent's own event
// queue for a fresh Decide pass, carrying the triggering action's thoughts.
func (r *agentRuntime) sendJinshuListed(act action.Action, query string, page int, items []eventqueue.JinshuListItem) {
	payload := &eventqueue.JinshuListedPayload{Query: query, Page: page, Results: items}
	if err := r.emitSelfHeldResult(eventqueue.EventTypeJinshuListed, model.EventTypeJinshuListed, 0, payload, act.ID,
		&eventqueue.TriggerAction{Background: act.Background, Reason: act.Reason}); err != nil {
		applogger.Error("sendJinshuListed: failed to persist result", "action_id", act.ID, "error", err)
	}
}

// handleListSentJinshu runs the paginated keyword search over the agent's sent
// jinshu and reflows the result back as a JinshuSentListed event so the agent
// can recall what it has already delivered.
func (r *agentRuntime) handleListSentJinshu(act action.Action) {
	defer endActionLogged(act.ID, "list_sent_jinshu")
	params := act.ListSentJinshuParams

	page := params.Page
	if page < 1 {
		page = 1
	}
	limit := params.Limit
	if limit < 1 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	records, err := dops.SearchSentJinshu(r.agentPersonID, params.Query, (page-1)*limit, limit)
	if err != nil {
		applogger.Error("list_sent_jinshu: search failed",
			"person_id", r.agentPersonID, "query", params.Query, "error", err)
		r.sendJinshuSentListed(act, params.Query, page, nil)
		return
	}

	// Resolve recipient names in one batch.
	idSet := make(map[int64]struct{})
	for _, rec := range records {
		idSet[rec.ToPersonID] = struct{}{}
	}
	ids := make([]int64, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	names, err := dops.GetPersonNames(ids)
	if err != nil {
		applogger.Error("list_sent_jinshu: failed to resolve recipient names", "error", err)
		names = map[int64]string{}
	}

	items := make([]eventqueue.JinshuSentListItem, 0, len(records))
	for _, rec := range records {
		toName := names[rec.ToPersonID]
		if toName == "" {
			toName = fmt.Sprintf("person_%d", rec.ToPersonID)
		}
		items = append(items, eventqueue.JinshuSentListItem{
			JinshuID:  rec.ID,
			ToName:    toName,
			Topic:     rec.Topic,
			CreatedAt: rec.CreatedAt.Format("2006-01-02 15:04"),
		})
	}

	r.sendJinshuSentListed(act, params.Query, page, items)
}

// sendJinshuSentListed dispatches the sent-list result back to the agent's own
// event queue for a fresh Decide pass, carrying the triggering action's thoughts.
func (r *agentRuntime) sendJinshuSentListed(act action.Action, query string, page int, items []eventqueue.JinshuSentListItem) {
	payload := &eventqueue.JinshuSentListedPayload{Query: query, Page: page, Results: items}
	if err := r.emitSelfHeldResult(eventqueue.EventTypeJinshuSentListed, model.EventTypeJinshuSentListed, 0, payload, act.ID,
		&eventqueue.TriggerAction{Background: act.Background, Reason: act.Reason}); err != nil {
		applogger.Error("sendJinshuSentListed: failed to persist result", "action_id", act.ID, "error", err)
	}
}

// handleSendJinshu delivers selected Agent Owned Space resources to another
// person as a jinshu and reflows the outcome back as a JinshuSent event so the
// agent knows whether the delivery succeeded.
func (r *agentRuntime) handleSendJinshu(act action.Action) {
	defer endActionLogged(act.ID, "send_jinshu")
	plan := act.SendJinshuPlan

	if plan.ToPersonID == r.agentPersonID {
		applogger.Error("send_jinshu: recipient is self, skipping",
			"agent_config_id", r.agentConfigID)
		r.sendJinshuSent(act, 0, "", plan.Topic, "failure", "cannot send a jinshu to yourself")
		return
	}

	toName := ""
	if to, err := dops.GetPerson(plan.ToPersonID); err != nil {
		applogger.Error("send_jinshu: failed to load recipient",
			"to_person_id", plan.ToPersonID, "error", err)
		toName = fmt.Sprintf("person_%d", plan.ToPersonID)
	} else {
		toName = to.Name
	}

	workDir := privatespace.GetWorkDirPath(r.agentPersonID)
	files, _, err := workspace.ResolveAOSFiles(r.agentPersonID, workDir, plan.Paths)
	if err != nil {
		applogger.Error("send_jinshu: failed to resolve paths",
			"agent_config_id", r.agentConfigID, "error", err)
		r.sendJinshuSent(act, 0, toName, plan.Topic, "failure", err.Error())
		return
	}

	record, err := jinshu.Send(jinshu.SendParams{
		FromPersonID: r.agentPersonID,
		ToPersonID:   plan.ToPersonID,
		Topic:        plan.Topic,
		Description:  plan.Description,
		Files:        files,
	})
	if err != nil {
		applogger.Error("send_jinshu: send failed",
			"agent_config_id", r.agentConfigID, "error", err)
		r.sendJinshuSent(act, 0, toName, plan.Topic, "failure", err.Error())
		return
	}

	r.sendJinshuSent(act, record.ID, toName, record.Topic, "success", "")
}

// sendJinshuSent dispatches the delivery outcome back to the agent's own event
// queue for a fresh Decide pass, carrying the triggering action's thoughts.
func (r *agentRuntime) sendJinshuSent(act action.Action, jinshuID int64, toName, topic, status, errMsg string) {
	payload := &eventqueue.JinshuSentPayload{JinshuID: jinshuID, ToName: toName, Topic: topic, Status: status, Error: errMsg}
	trigger := &eventqueue.TriggerAction{Background: act.Background, Reason: act.Reason}
	var err error
	if jinshuID > 0 {
		err = r.emitReferencedResult(eventqueue.EventTypeJinshuSent, model.EventTypeJinshuSent, 0, jinshuID, payload, act.ID, model.ActionEffectJinshu, trigger)
	} else {
		err = r.emitSelfHeldResult(eventqueue.EventTypeJinshuSent, model.EventTypeJinshuSent, 0, payload, act.ID, trigger)
	}
	if err != nil {
		applogger.Error("sendJinshuSent: failed to persist outcome", "action_id", act.ID, "error", err)
	}
}

// sendJinshuReadCompleted dispatches the read result back to the agent's own
// event queue for a fresh Decide pass.
func (r *agentRuntime) sendJinshuReadCompleted(act action.Action, jinshuID int64, fromName, topic, summary string, readErr error) {
	payload := &eventqueue.JinshuReadCompletedPayload{
		JinshuID: jinshuID,
		FromName: fromName,
		Topic:    topic,
		Summary:  summary,
		Status:   "success",
	}
	if readErr != nil {
		payload.Status = "failure"
		payload.Error = readErr.Error()
	}

	if err := r.emitSelfHeldResult(eventqueue.EventTypeJinshuReadCompleted, model.EventTypeJinshuReadCompleted, 0, payload, act.ID, nil); err != nil {
		applogger.Error("sendJinshuReadCompleted: failed to persist outcome", "action_id", act.ID, "error", err)
	}
}

// formatJinshuFileEntries renders the jinshu file listing for the loop's
// initial prompt so the agent knows which paths it can read.
func formatJinshuFileEntries(entries []jinshu.FileEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, e := range entries {
		if e.IsDir {
			fmt.Fprintf(&sb, "%s/\n", e.Path)
		} else {
			fmt.Fprintf(&sb, "%s (%d bytes)\n", e.Path, e.Size)
		}
	}
	return sb.String()
}

// executeChat runs a Chat action asynchronously without creating a Work.
// It generates the reply and hands it to the serial message commit worker;
// the Action ends when that worker commits, or here if handoff fails.
func (r *agentRuntime) executeChat(ctx context.Context, situation *Situation, plan *action.ChatPlan, actionID int64, activeWorks []*work) {
	queued := false
	defer func() {
		if !queued {
			if err := endAction(actionID); err != nil {
				applogger.Error("executeChat: failed to end unsuccessful action", "action_id", actionID, "error", err)
			}
		}
	}()
	event := situation.Matter.Event

	// A work always completes in the session where it was created. Anchor
	// the reply to that session so the notification never lands in a new or
	// unrelated conversation, regardless of what the Decide LLM selected.
	if event != nil && event.Type == eventqueue.EventTypeWorkCompleted && event.SessionID > 0 {
		if plan.SessionID != event.SessionID {
			applogger.Info("executeChat: anchoring work-completed reply to origin session",
				"agent_config_id", r.agentConfigID,
				"llm_session_id", plan.SessionID,
				"origin_session_id", event.SessionID,
			)
		}
		plan.SessionID = event.SessionID
	}

	if plan.SessionID == 0 {
		applogger.Error("executeChat: session_id is 0 (invalid), skipping",
			"agent_config_id", r.agentConfigID,
		)
		return
	}

	var targetSessionID int64
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

	// Fast path: scheduled send_message carries pre-computed content. Commit
	// it directly without loading session context or running the LLM pipeline.
	if plan.Content != "" {
		r.weakUpdateAgentStatusInSession(targetSessionID, model.ParticipantStatusWorking)
		defer r.weakUpdateAgentStatusInSession(targetSessionID, model.ParticipantStatusIdle)
		queued = r.queueChatCommit(ctx, &commitRequest{
			actionID:              actionID,
			sessionID:             targetSessionID,
			content:               plan.Content,
			expressionInstruction: plan.ExpressionInstruction,
		})
		return
	}

	session, err := dops.GetSession(targetSessionID)
	if err != nil {
		applogger.Error("executeChat: failed to load session",
			"session_id", targetSessionID, "error", err)
		return
	}

	// A source-session message is only a chat trigger in that same session.
	// Cross-session actions must build context from their target session.
	var trigger *chat.Trigger
	if event != nil {
		if payload, ok := event.Payload.(*eventqueue.ScheduledEventPayload); ok {
			trigger = &chat.Trigger{
				Type: chat.TriggerAlarm,
				Alarm: &chat.TriggerAlarmData{
					SelfReminder: payload.Message,
				},
			}
		} else if event.Type == eventqueue.EventTypeNewPrivateChatMessage && event.SessionID == targetSessionID {
			trigger = &chat.Trigger{Type: chat.TriggerMessage}
		}
	}

	var chatCtx *chat.ChatContext
	var readMessageRange [2]int64
	if event != nil && event.SessionID == targetSessionID && comprehension != nil && comprehension.Chat != nil {
		chatCtx = &chat.ChatContext{
			PersonState:     comprehension.Chat.PersonState,
			HistoryKeywords: historyKeywords(comprehension.Chat.HistorySearch),
			KBSegments:      kbSegments(comprehension.Chat.KBRetrieval),
		}
		readMessageRange = comprehension.Chat.ReadMessageRange
	}
	if targetSessionID > 0 {
		if chatCtx == nil {
			chatCtx = &chat.ChatContext{}
		}
		focusHint := ""
		if event != nil && event.SessionID == targetSessionID {
			focusHint = event.FormatDescription()
		}
		chatCtx.FocusContext = buildSessionFocusContext(r.agentPersonID, targetSessionID, activeWorks, focusHint)
	}

	// A work-completed event carries the FocusedLoop's final summary in its
	// payload. Surface it into the chat context so the agent can reference
	// what was actually produced when notifying the user.
	if event != nil {
		if payload, ok := event.Payload.(*eventqueue.WorkCompletedPayload); ok && payload != nil {
			if chatCtx == nil {
				chatCtx = &chat.ChatContext{}
			}
			chatCtx.FocusedWorkResult = &focusedwork.FocusedWorkResult{
				Status: payload.Status,
				Output: payload.WorkOutput,
				Error:  payload.WorkError,
			}
		}
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

	queued = r.queueChatCommit(ctx, &commitRequest{
		actionID:              actionID,
		sessionID:             targetSessionID,
		content:               result.Content,
		expressionInstruction: result.ExpressionInstruction,
	})
}

// queueChatCommit hands a completed generation to the serial commit worker.
// Cancellation before handoff leaves the Action for executeChat to end.
func (r *agentRuntime) queueChatCommit(ctx context.Context, request *commitRequest) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case r.messageCommitCh <- request:
		return true
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

// newWork creates a new Focus from a Situation and an action.Action, persists it to the
// database, and returns the work object. Only used for StartFocusedWork actions.
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
	if tx.Error != nil {
		applogger.Error("Failed to begin work transaction", "agent_config_id", r.agentConfigID, "session_id", targetSessionID, "error", tx.Error)
		return nil, false
	}

	// The work's Description should reflect what the agent intends to DO
	// (its guidance), not merely what triggered it. Using the triggering
	// event description breaks down for retries: a work created in response
	// to a WorkCompleted event would otherwise record the previous work's
	// completion status as its own description.
	workDescription := plan.Guidance
	if workDescription == "" {
		if event != nil {
			workDescription = event.FormatDescription()
		} else {
			workDescription = situation.Matter.Description
		}
	}
	workRecord := &model.Work{
		PersonID:    r.agentPersonID,
		SessionID:   targetSessionID,
		Description: workDescription,
		Status:      model.WorkStatusRunning,
		FocusPhase:  model.FocusPhaseExecuting,
		Checkpoint:  "Focus created and awaiting execution.",
	}
	if err := tx.Create(workRecord).Error; err != nil {
		tx.Rollback()
		applogger.Error("Failed to create work", "agent_config_id", r.agentConfigID, "session_id", targetSessionID, "error", err)
		return nil, false
	}
	if err := recordActionEffect(tx, dec.ID, model.ActionEffectWork, workRecord.ID); err != nil {
		tx.Rollback()
		applogger.Error("Failed to record work action source", "action_id", dec.ID, "work_id", workRecord.ID, "error", err)
		return nil, false
	}
	if err := tx.Model(&model.Action{}).Where("id = ?", dec.ID).
		Update("status", model.ActionStatusEnded).Error; err != nil {
		tx.Rollback()
		applogger.Error("Failed to finish start-work action", "action_id", dec.ID, "error", err)
		return nil, false
	}

	if err := tx.Commit().Error; err != nil {
		applogger.Error("Failed to commit work transaction", "agent_config_id", r.agentConfigID, "error", err)
		return nil, false
	}
	refreshMemorySource(model.MemorySourceWork, workRecord.ID)
	refreshMemorySource(model.MemorySourceAction, dec.ID)

	// Build the runtime context only after the create transaction has been
	// committed. buildSessionFocusContext performs regular DB reads; running it
	// while the transaction still owns the single SQLite connection would
	// self-block until GORM's default context timeout.
	w := &work{
		ID:            workRecord.ID,
		agent:         r,
		sessionID:     targetSessionID,
		plan:          plan,
		maxIterations: 90,
		focusContext:  buildSessionFocusContext(r.agentPersonID, targetSessionID, r.activeWorks, plan.Guidance),
		comprehension: comprehension,
		guidanceCh:    make(chan focusedwork.GuidanceDirective, 8),
		done:          make(chan struct{}),
		triggerAction: &eventqueue.TriggerAction{
			Background: dec.Background,
			Reason:     dec.Reason,
		},
	}

	applogger.Info("Work created",
		"work_id", w.ID,
		"agent_config_id", r.agentConfigID,
		"session_id", w.sessionID,
	)

	return w, true
}

// buildMetadata constructs system-generated Metadata from the triggering event.
// This is used by the FocusedLoop to understand its origin (session, self-reminder, etc.)
// and to power tools like search_chat_histories with the correct session context.
// Returns nil when event is nil (heartbeat-triggered work has no event metadata).
func buildMetadata(event *eventqueue.AgentEvent) *focusedwork.Metadata {
	if event == nil {
		return nil
	}
	switch event.Type {
	case eventqueue.EventTypeNewPrivateChatMessage:
		if payload, ok := event.Payload.(*eventqueue.NewMessagePayload); ok {
			return &focusedwork.Metadata{
				SourceType: focusedwork.SourceTypeSession,
				SessionMeta: &focusedwork.SessionMeta{
					SessionID:  event.SessionID,
					Trigger:    fmt.Sprintf("%s sent a chat message: %q", payload.SpeakerName, payload.MessageContent),
					SenderName: payload.SpeakerName,
				},
			}
		}
	case eventqueue.EventTypeScheduled:
		return &focusedwork.Metadata{
			SourceType: focusedwork.SourceTypeScheduled,
		}
	case eventqueue.EventTypeWorkCompleted:
		trigger := "a previous work completed"
		if event.TriggerAction != nil {
			trigger = event.TriggerAction.Background
		}
		return &focusedwork.Metadata{
			SourceType: focusedwork.SourceTypeWorkCompleted,
			SessionMeta: &focusedwork.SessionMeta{
				SessionID: event.SessionID,
				Trigger:   trigger,
			},
		}
	}
	return nil
}

// alarmTriggerAtFormat is the only accepted time format for AlarmPlan.TriggerAt.
const alarmTriggerAtFormat = "2006-01-02 15:04:05"

// handleCreateAlarmAction executes a action.CreateAlarm action.
func (r *agentRuntime) handleCreateAlarmAction(act action.Action, situation *Situation) {
	plan := act.AlarmPlan
	actionID := act.ID
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
	if action == model.ScheduledEventActionSendMessage {
		if strings.TrimSpace(plan.ActionContent) == "" {
			applogger.Error("action.CreateAlarm: 'send_message' action requires action_content, skipping",
				"agent_config_id", r.agentConfigID,
			)
			return
		}
		if strings.TrimSpace(plan.ExpressionInstruction) == "" {
			applogger.Error("action.CreateAlarm: 'send_message' action requires expression_instruction, skipping",
				"agent_config_id", r.agentConfigID,
			)
			return
		}
		// A send_message alarm commits pre-computed content directly into its
		// origin session. Without a session anchor there is nowhere to deliver
		// it — such an alarm would be silently dropped at trigger time
		// (executeChat rejects a zero session), so reject it here instead.
		if sessionID == 0 {
			applogger.Error("action.CreateAlarm: 'send_message' action requires a session anchor, skipping",
				"agent_config_id", r.agentConfigID,
			)
			return
		}
	}

	record := model.ScheduledEvent{
		PersonID:              r.agentPersonID,
		SessionID:             sessionID,
		TriggerAt:             triggerAt,
		Message:               plan.Message,
		Action:                action,
		ActionContent:         plan.ActionContent,
		ExpressionInstruction: plan.ExpressionInstruction,
		Status:                model.ScheduledEventStatusPending,
	}
	tx := database.DB.Begin()
	if tx.Error != nil {
		applogger.Error("action.CreateAlarm: failed to begin transaction", "action_id", actionID, "error", tx.Error)
		return
	}
	defer tx.Rollback()
	if err := tx.Create(&record).Error; err != nil {
		applogger.Error("action.CreateAlarm: failed to create scheduled event record",
			"agent_config_id", r.agentConfigID,
			"person_id", r.agentPersonID,
			"error", err,
		)
		return
	}
	if err := recordActionEffect(tx, actionID, model.ActionEffectScheduledEvent, record.ID); err != nil {
		applogger.Error("action.CreateAlarm: failed to record action source", "action_id", actionID, "error", err)
		return
	}
	if err := tx.Model(&model.Action{}).Where("id = ?", actionID).
		Update("status", model.ActionStatusEnded).Error; err != nil {
		applogger.Error("action.CreateAlarm: failed to end action", "action_id", actionID, "error", err)
		return
	}
	if err := tx.Commit().Error; err != nil {
		applogger.Error("action.CreateAlarm: failed to commit alarm result", "action_id", actionID, "error", err)
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

	notify(notification.AgentStatusChanged{SessionID: sessionID, PersonID: r.agentPersonID, Status: status})
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
func createAgentRuntime(agentConfigID int64) (*agentRuntime, error) {
	eventCh := eventqueue.Subscribe(agentConfigID)

	runtime := newAgentRuntime(agentConfigID, eventCh, 30*time.Second)

	// Resolve agent's PersonID for participant_session queries
	ac, err := dops.Get[model.AgentConfig](agentConfigID)
	if err != nil {
		return nil, fmt.Errorf("createAgentRuntime: failed to load agent config %d: %w", agentConfigID, err)
	}
	runtime.agentPersonID = ac.PersonID

	// Ensure the agent has the stable AOS skeleton before it can inspect or
	// enter owned space. This is idempotent and also repairs agents created by
	// older versions that only had session-specific directories.
	if err := workspace.InitAgentOwnedSpace(ac.PersonID); err != nil {
		return nil, fmt.Errorf("createAgentRuntime: initialize AOS for person %d: %w", ac.PersonID, err)
	}

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

// historyKeywords passes Comprehend's retrieval plan to the target chat context.
func historyKeywords(search *comprehendTypes.HistorySearch) []string {
	if search == nil {
		return nil
	}
	return search.Keywords
}

// kbSegments passes retrieved knowledge-base excerpts to chat assembly.
func kbSegments(retrieval *comprehendTypes.KBRetrieval) []comprehendTypes.Segment {
	if retrieval == nil {
		return nil
	}
	return retrieval.Segments
}
