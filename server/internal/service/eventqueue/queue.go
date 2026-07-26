// Package eventqueue provides a stateful, in-process event bus for the agent
// runtime system. It decouples event producers (handlers, tools, scheduled
// events) from event consumers (agent runtimes) so that neither side needs
// to depend on the other.
//
// Dependency direction:
//
//	handler  → eventqueue ← runtime
//	task/tools → eventqueue   (wake_me_when goroutine calls SendEvent directly)
//	chat     → eventqueue     (replaces the old agentevent.NotifyAgentNewMessage)
//
// The package owns the event type definitions (AgentEvent, payload types) and
// the per-agent channel lifecycle.  Producers call SendEvent; consumers call
// Subscribe to obtain a read-only channel and Unsubscribe when shutting down.
package eventqueue

import (
	"sync"

	applogger "qingqiu-world-server/internal/logger"
)

// ---------------------------------------------------------------------------
// eventQueue — the stateful event bus
// ---------------------------------------------------------------------------

const channelSize = 64 // Per-agent event channel buffer size

// eventQueue is a stateful, in-process event bus that maintains per-agent
// event channels.  It is the single routing point between event producers
// and agent runtime consumers.
//
// Thread-safe.  All public methods may be called concurrently.
type eventQueue struct {
	mu    sync.RWMutex
	chans map[int64]chan *AgentEvent // agentConfigID -> event channel
}

// global is the singleton eventQueue instance.
// Initialized once during application startup via InitGlobal.
var global *eventQueue

// Init creates and sets the global eventQueue singleton.
func Init() {
	global = newEventQueue()
	applogger.Info("Global event queue initialized")
}

// SendEvent sends an event to the given agent via the global event queue.
func SendEvent(agentConfigID int64, event *AgentEvent) {
	global.SendEvent(agentConfigID, event)
}

// Subscribe returns the event channel for the given agent via the global queue.
func Subscribe(agentConfigID int64) <-chan *AgentEvent {
	return global.Subscribe(agentConfigID)
}

// Unsubscribe removes the event channel for the given agent via the global queue.
func Unsubscribe(agentConfigID int64) {
	global.Unsubscribe(agentConfigID)
}

// newEventQueue creates a new eventQueue.
func newEventQueue() *eventQueue {
	return &eventQueue{
		chans: make(map[int64]chan *AgentEvent),
	}
}

// Subscribe creates (if needed) and returns the event channel for the given
// agent.  The caller (typically AgentRuntime) should consume from this
// channel in its event loop.
//
// Returns a read-only channel.  The channel is buffered (channelSize) and is
// unique per agent — calling Subscribe multiple times for the same agent
// returns the same channel.
func (q *eventQueue) Subscribe(agentConfigID int64) <-chan *AgentEvent {
	q.mu.Lock()
	defer q.mu.Unlock()

	if ch, ok := q.chans[agentConfigID]; ok {
		return ch
	}

	ch := make(chan *AgentEvent, channelSize)
	q.chans[agentConfigID] = ch
	return ch
}

// Unsubscribe removes the event channel for the given agent and drains any
// remaining events.  Called when an agent runtime shuts down.
func (q *eventQueue) Unsubscribe(agentConfigID int64) {
	q.mu.Lock()
	defer q.mu.Unlock()

	ch, ok := q.chans[agentConfigID]
	if !ok {
		return
	}

	// Drain remaining events to prevent goroutine leak
	for {
		select {
		case <-ch:
		default:
			close(ch)
			delete(q.chans, agentConfigID)
			return
		}
	}
}

// SendEvent sends an event to the given agent's event channel.
// Non-blocking: drops the event if the channel is full.
// If no agent is subscribed, the event is silently dropped with a warning.
//
// Uses recover to prevent panic on send to closed channel during shutdown
// edge cases (work goroutine defer races with Unsubscribe).
func (q *eventQueue) SendEvent(agentConfigID int64, event *AgentEvent) {
	defer func() {
		if r := recover(); r != nil {
			applogger.Error("SendEvent recovered from panic (channel likely closed during shutdown)",
				"agent_config_id", agentConfigID, "event_type", event.Type, "panic", r)
		}
	}()

	q.mu.RLock()
	ch, ok := q.chans[agentConfigID]
	q.mu.RUnlock()

	if !ok {
		applogger.Error("No subscriber for agent event, dropping",
			"agent_config_id", agentConfigID,
			"event_type", event.Type,
			"session_id", event.SessionID,
		)
		return
	}

	select {
	case ch <- event:
	default:
		applogger.Error("Agent event channel full, dropping event",
			"agent_config_id", agentConfigID,
			"event_type", event.Type,
			"session_id", event.SessionID,
		)
	}
}
