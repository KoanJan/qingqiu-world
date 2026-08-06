// Package runtime implements the Agent Runtime: the event-driven execution
// engine that transforms an Agent from a passive configuration object into
// an active, stateful entity with its own lifecycle.
//
// This file provides the runtimeManager which manages agentRuntime instances
// across all agents in the system. It serves as the bridge between the
// handler layer and the per-agent event loops.
package runtime

import (
	"context"
	"sync"
	"time"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/memory"
)

// runtimeManager manages agentRuntime instances for all agents.
// Thread-safe. Each agent gets exactly one runtime.
type runtimeManager struct {
	mu             sync.RWMutex
	runtimes       map[int64]*agentRuntime // agentConfigID -> runtime
	onStatusChange func(agentConfigID, personID, sessionID int64, status int)

	// rootCtx is the root context for all agent runtimes.
	// Cancelling it propagates to every runtime, stopping all goroutines at once.
	rootCtx   context.Context
	cancelAll context.CancelFunc

	// wg tracks all agent event loop goroutines.
	// Shutdown() waits on this to ensure all internal goroutines finish.
	wg sync.WaitGroup
}

// newRuntimeManager creates a new runtime manager.
func newRuntimeManager(onStatusChange func(agentConfigID, personID, sessionID int64, status int)) *runtimeManager {
	rootCtx, cancelAll := context.WithCancel(context.Background())
	return &runtimeManager{
		runtimes:       make(map[int64]*agentRuntime),
		onStatusChange: onStatusChange,
		rootCtx:        rootCtx,
		cancelAll:      cancelAll,
	}
}

// StartRuntime creates a runtime for the given agent, starts the event loop,
// and registers it. Does nothing if the runtime already exists.
func (rm *runtimeManager) StartRuntime(agentConfigID int64) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if _, ok := rm.runtimes[agentConfigID]; ok {
		return
	}

	rt, err := createAgentRuntime(agentConfigID, rm.onStatusChange)
	if err != nil {
		applogger.Error("StartRuntime: failed to create agent runtime", "agent_config_id", agentConfigID, "error", err)
		return
	}
	rm.wg.Add(1)
	go func() {
		defer rm.wg.Done()
		rt.Run(rm.rootCtx)
	}()
	rm.runtimes[agentConfigID] = rt
}

// StopAll signals all agent runtimes to stop but does NOT wait for them to finish.
// Use Shutdown() for graceful shutdown that waits for goroutine completion.
//
// Cancelling the root context propagates to every runtime, stopping the
// event loop, commit handler, and causing all active works to abandon.
func (rm *runtimeManager) StopAll() {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	applogger.Info("Signalling all agent runtimes to stop", "count", len(rm.runtimes))

	// Cancel the root context — all derived runtime contexts cancel automatically
	if rm.cancelAll != nil {
		rm.cancelAll()
	}

	// Cancel all alarm goroutines — they are not tied to the root context
	// because they need to survive across runtime restarts (via DB recovery).
	CancelAlarms()
}

// Shutdown gracefully shuts down all agent runtimes, waiting for all
// internal goroutines (event loops, works, draft handlers) to finish.
//
// After Shutdown returns, it is safe to Unsubscribe from the event queue
// and close channels without risk of panic from pending work goroutines.
func (rm *runtimeManager) Shutdown(timeout time.Duration) {
	// Phase 1: Signal all runtimes to stop
	rm.StopAll()

	// Phase 2: Wait for all event loop goroutines to return.
	// Each event loop waits for its active works and draft handler
	// before returning, so when wg.Wait() completes, everything is done.
	done := make(chan struct{})
	go func() {
		rm.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		applogger.Info("All agent runtimes exited cleanly")
	case <-time.After(timeout):
		applogger.Error("Agent runtimes shutdown timed out", "timeout", timeout)
	}

	// Phase 3: Safe to clean up channels.
	// All work goroutines have finished — none will try to send events.
	rm.mu.Lock()
	for agentConfigID := range rm.runtimes {
		eventqueue.Unsubscribe(agentConfigID)
	}
	rm.runtimes = make(map[int64]*agentRuntime)
	rm.mu.Unlock()
}

// globalRuntimeManager is the singleton runtime manager for the application.
// Initialized during application startup via Start.
var globalRuntimeManager *runtimeManager

// Shutdown gracefully shuts down all agent runtimes.
func Shutdown(timeout time.Duration) {
	if globalRuntimeManager != nil {
		globalRuntimeManager.Shutdown(timeout)
	}
}

// StartRuntime creates and starts a runtime for the given agent.
// Used when a new agent is created after initial startup.
// Does nothing if the runtime already exists.
func StartRuntime(agentConfigID int64) {
	if globalRuntimeManager != nil {
		globalRuntimeManager.StartRuntime(agentConfigID)
	}
}

// Start initializes the global runtime system: event queue, all agent runtimes,
// and the output callbacks for SSE push.  Must be called once during application
// startup, after database.Init() and before any handler traffic.
//
// All agents are eagerly started at startup.  For a desktop local application
// the number of agents is small and the resource cost is negligible, so there
// is no reason to use lazy initialization — every agent should be ready to
// receive events from the moment the server starts.
//
// The three callbacks connect the runtime to the SSE transport layer:
//   - onStatusChange: agent heartbeat / status transitions
//   - onPushMessage: new message content to stream to UI
//   - onPushSSE: raw SSE events (notifications, etc.)
func Start(
	onStatusChange func(agentConfigID, personID, sessionID int64, status int),
	onPushMessage func(sessionID, messageID, personID int64, content string),
	onPushSSE func(sessionID int64, data string),
) {
	pushMessageEvent = onPushMessage
	pushSSEEvent = onPushSSE

	globalRuntimeManager = newRuntimeManager(onStatusChange)

	// Reset any stale working statuses left from a previous crash.
	// Each agent's recoverActiveWorks handles the normal case (work record + status),
	// but this global sweep covers the edge case where setStatus(working) was called
	// and a crash occurred before the work record was persisted.
	if err := database.DB.Model(&model.ParticipantSession{}).
		Where("participant_id IN (SELECT id FROM persons WHERE type = ?) AND status = ?", model.PersonTypeAI, model.ParticipantStatusWorking).
		Update("status", model.ParticipantStatusIdle).Error; err != nil {
		applogger.Error("Failed to reset stale participant statuses on startup", "error", err)
	}

	// Eagerly start runtimes for all agent configs
	var configs []model.AgentConfig
	if err := database.DB.Find(&configs).Error; err != nil {
		applogger.Error("Failed to load agent configs for runtime initialization", "error", err)
	} else {
		for _, ac := range configs {
			globalRuntimeManager.StartRuntime(ac.ID)
		}
		applogger.Info("All agent runtimes started", "count", len(configs))
	}

	// Recover scheduled events that were orphaned by the previous shutdown.
	// Must be called after all agent runtimes have started so that recovered
	// events can be routed to their subscribed channels.
	recoverScheduledEvents()
}

// SendNewMessageEvent records a memory event for a user message and dispatches
// it to the agent runtime via the event queue. This is the production +
// distribution entry point for user messages — the handler calls this instead
// of calling memory or eventqueue directly.
//
// Production: creates a memory event record (sync) + enqueues embedding (async).
// Distribution: sends the event to the agent's event queue channel.
//
// The agent runtime event loop is the consumer — it receives the event and
// calls memory.CreateObservation using the EventID carried in the payload.
func SendNewMessageEvent(agentConfigID, sessionID, messageID int64, content, speakerName string) {
	// Production: record memory event before dispatching.
	eventID, err := memory.RecordEvent(messageID, content)
	if err != nil {
		applogger.Error("failed to record memory event for user message",
			"message_id", messageID, "error", err)
	}

	// Distribution: notify agent runtime.
	event := &eventqueue.AgentEvent{
		Type:      eventqueue.EventTypeNewPrivateChatMessage,
		SessionID: sessionID,
		EventID:   eventID,
		Payload: &eventqueue.NewMessagePayload{
			MessageID:      messageID,
			MessageContent: content,
			SpeakerName:    speakerName,
		},
	}
	eventqueue.SendEvent(agentConfigID, event)
}
