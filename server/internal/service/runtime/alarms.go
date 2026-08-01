package runtime

import (
	"context"
	"sync"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"

	applogger "qingqiu-world-server/internal/logger"
)

// alarmRegistry manages all active alarm goroutines, allowing them to be
// cancelled collectively (e.g., on server shutdown) or individually.
//
// The registry is keyed by scheduledEventID. Each entry holds a CancelFunc
// that, when invoked, cancels the goroutine's context — causing it to exit
// cleanly without firing.
var alarmRegistry = &alarmRegistryType{}

type alarmRegistryType struct {
	mu     sync.Mutex
	alarms map[int64]context.CancelFunc // scheduledEventID -> cancel
}

// register stores a cancel function for an alarm goroutine.
func (r *alarmRegistryType) register(eventID int64, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.alarms == nil {
		r.alarms = make(map[int64]context.CancelFunc)
	}
	r.alarms[eventID] = cancel
}

// unregister removes an alarm from the registry (after it fires or is cancelled).
func (r *alarmRegistryType) unregister(eventID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.alarms, eventID)
}

// cancelAll cancels all registered alarm goroutines. Called on server shutdown.
func (r *alarmRegistryType) cancelAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, cancel := range r.alarms {
		cancel()
		delete(r.alarms, id)
	}
}

// CancelAlarms shuts down all alarm goroutines. Called during graceful shutdown
// via runtime.StopAll().
func CancelAlarms() {
	alarmRegistry.cancelAll()
}

// registerAlarmGoroutine spawns a goroutine that waits until the scheduled
// event's trigger_at, then fires it. The goroutine is tracked in alarmRegistry
// for cancellation on shutdown.
//
// The goroutine:
//  1. Waits until trigger_at (or cancellation)
//  2. Re-checks DB status (may have been cancelled while waiting)
//  3. Marks the event as Triggered
//  4. Sends an EventTypeScheduled event through eventqueue
func registerAlarmGoroutine(event *model.ScheduledEvent) {
	alarmCtx, alarmCancel := context.WithCancel(context.Background())
	alarmRegistry.register(event.ID, alarmCancel)

	go func() {
		defer alarmRegistry.unregister(event.ID)

		until := time.Until(event.TriggerAt)
		applogger.Info("Scheduled event goroutine waiting",
			"event_id", event.ID,
			"person_id", event.PersonID,
			"session_id", event.SessionID,
			"trigger_at", event.TriggerAt,
			"action", event.Action,
			"wait_duration", until.Round(time.Second),
		)

		timer := time.NewTimer(until)
		defer timer.Stop()

		select {
		case <-alarmCtx.Done():
			applogger.Info("Scheduled event goroutine cancelled",
				"event_id", event.ID,
				"person_id", event.PersonID,
			)
			return
		case <-timer.C:
			// Timer fired, proceed to trigger the alarm
		}

		// Re-check DB status before firing — the event may have been
		// cancelled in the database while we were waiting.
		var currentEvent model.ScheduledEvent
		if err := database.DB.First(&currentEvent, event.ID).Error; err != nil {
			applogger.Error("Scheduled event not found, skipping",
				"event_id", event.ID, "error", err)
			return
		}
		if currentEvent.Status != model.ScheduledEventStatusPending {
			applogger.Info("Scheduled event no longer pending, skipping",
				"event_id", event.ID, "status", currentEvent.Status)
			return
		}

		fireScheduledEvent(event)
	}()
}

// fireScheduledEvent marks a scheduled event as triggered and sends it through
// the eventqueue. Used for both normal goroutine triggering and overdue
// recovery during startup.
func fireScheduledEvent(event *model.ScheduledEvent) {
	if err := database.DB.Model(&model.ScheduledEvent{}).
		Where("id = ?", event.ID).
		Update("status", model.ScheduledEventStatusTriggered).Error; err != nil {
		applogger.Error("fireScheduledEvent: failed to mark as triggered",
			"event_id", event.ID, "error", err)
		return
	}

	applogger.Info("Scheduled event fired, sending to eventqueue",
		"event_id", event.ID,
		"person_id", event.PersonID,
		"session_id", event.SessionID,
		"action", event.Action,
	)

	// Bridge: resolve agentConfigID from personID for eventqueue routing.
	// The event queue is keyed by agentConfigID because the runtime event loop
	// subscribes per-agent-config, but scheduled_events is now keyed by person_id.
	var ac model.AgentConfig
	if err := database.DB.Where("person_id = ?", event.PersonID).First(&ac).Error; err != nil {
		applogger.Error("fireScheduledEvent: failed to resolve agent config from person_id",
			"event_id", event.ID,
			"person_id", event.PersonID,
			"error", err,
		)
		return
	}

	eventqueue.SendEvent(ac.ID, &eventqueue.AgentEvent{
		Type:      eventqueue.EventTypeScheduled,
		SessionID: event.SessionID,
		Payload: &eventqueue.ScheduledEventPayload{
			ScheduledEventID: event.ID,
			Message:          event.Message,
			Action:           event.Action,
			ActionContent:    event.ActionContent,
		},
	})
}

// handleAlarmCreated processes an EventTypeAlarmCreated event by loading the
// scheduled event from the DB and registering a goroutine to wait for it.
//
// This is called from the runtime event loop when a tool creates a new alarm.
func (r *agentRuntime) handleAlarmCreated(eventID int64) {
	event := &model.ScheduledEvent{}
	if err := database.DB.First(event, eventID).Error; err != nil {
		applogger.Error("handleAlarmCreated: failed to load scheduled event",
			"event_id", eventID, "error", err)
		return
	}

	if event.Status != model.ScheduledEventStatusPending {
		applogger.Info("handleAlarmCreated: event not pending, skipping",
			"event_id", eventID, "status", event.Status)
		return
	}

	// If the trigger time has already passed (edge case: clock skew or delay),
	// fire immediately instead of registering a goroutine.
	if event.TriggerAt.Before(time.Now()) || event.TriggerAt.Equal(time.Now()) {
		applogger.Info("handleAlarmCreated: trigger time already passed, firing immediately",
			"event_id", eventID, "trigger_at", event.TriggerAt)
		fireScheduledEvent(event)
		return
	}

	registerAlarmGoroutine(event)
}

// recoverScheduledEvents restores pending scheduled events after a server restart.
// When the server shuts down, alarm goroutines are cancelled and in-flight
// eventqueue events are drained. This function recovers orphaned events by:
//   - Immediately triggering events whose trigger_at has already passed
//   - Re-registering goroutines for events whose trigger_at is still in the future
//
// Called during runtime startup, after eventqueue.Init() and runtime.Start().
func recoverScheduledEvents() {
	var pendingEvents []*model.ScheduledEvent
	if err := database.DB.Where("status = ?", model.ScheduledEventStatusPending).
		Order("trigger_at ASC").Find(&pendingEvents).Error; err != nil {
		applogger.Error("recoverScheduledEvents: failed to load pending events", "error", err)
		return
	}

	if len(pendingEvents) == 0 {
		return
	}

	now := time.Now()
	recovered := 0
	overdue := 0
	for _, event := range pendingEvents {
		if event.TriggerAt.Before(now) || event.TriggerAt.Equal(now) {
			fireScheduledEvent(event)
			overdue++
		} else {
			registerAlarmGoroutine(event)
		}
		recovered++
	}

	applogger.Info("recoverScheduledEvents: recovered scheduled events",
		"count", recovered,
		"overdue", overdue,
		"future", recovered-overdue,
	)
}
