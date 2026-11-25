package runtime

import (
	"time"

	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/service/eventqueue"
)

func (r *agentRuntime) bufferEvent(event *eventqueue.AgentEvent) error {
	if err := r.persistBufferedEvent(event); err != nil {
		return err
	}
	applogger.Info("buffered agent event due to insufficient energy",
		"person_id", r.agentPersonID,
		"event_type", event.Type,
		"session_id", event.SessionID,
		"event_id", event.EventID,
	)
	return dops.SetAgentSleepSinceIfEmpty(r.agentPersonID, time.Now())
}

// bufferInterruptedEvent retains an event returned by the queue but
// not accepted before shutdown. Unlike energy buffering, it does not put the
// agent to sleep; the queue will retrieve it on the next startup.
func (r *agentRuntime) bufferInterruptedEvent(event *eventqueue.AgentEvent) error {
	if event == nil || event.EventID <= 0 {
		return nil
	}
	accepted, err := hasAcceptedDecision(r.agentPersonID, event.EventID)
	if err != nil || accepted {
		return err
	}
	if err := r.persistBufferedEvent(event); err != nil {
		return err
	}
	applogger.Info("buffered shutdown-interrupted agent event", "person_id", r.agentPersonID, "event_id", event.EventID)
	return nil
}

// persistBufferedEvent restores the current delivery through the queue storage
// boundary, preserving its original order and invalidating the queue's cache.
func (r *agentRuntime) persistBufferedEvent(event *eventqueue.AgentEvent) error {
	return r.queue.Restore(event)
}
