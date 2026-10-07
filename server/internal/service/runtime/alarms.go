package runtime

import (
	"context"
	"sync"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/memory"

	applogger "qingqiu-world-server/internal/logger"
)

// alarmRegistry manages all active alarm goroutines, allowing them to be
// cancelled collectively (e.g., on server shutdown) or individually.
//
// The registry is keyed by scheduledEventID. Each entry holds a CancelFunc
// that, when invoked, cancels the goroutine's context — causing it to exit
// cleanly without firing.
var alarmRegistry = &alarmRegistryType{}

// alarmRegistryType serializes registration and cancellation of alarm waiters.
type alarmRegistryType struct {
	mu     sync.Mutex
	alarms map[int64]*alarmRegistration // scheduledEventID -> current waiter
}

// alarmRegistration identifies the exact waiter that owns a scheduled alarm.
type alarmRegistration struct {
	cancel context.CancelFunc
}

// register stores a cancel function only if this alarm has no waiter yet.
func (r *alarmRegistryType) register(eventID int64, registration *alarmRegistration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.alarms == nil {
		r.alarms = make(map[int64]*alarmRegistration)
	}
	if _, exists := r.alarms[eventID]; exists {
		return false
	}
	r.alarms[eventID] = registration
	return true
}

// unregister removes only the waiter that actually exited; an old cancelled
// goroutine cannot remove a newer waiter registered during a quick restart.
func (r *alarmRegistryType) unregister(eventID int64, registration *alarmRegistration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.alarms[eventID] == registration {
		delete(r.alarms, eventID)
	}
}

// cancelAll cancels all registered alarm goroutines. Called on server shutdown.
func (r *alarmRegistryType) cancelAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, registration := range r.alarms {
		registration.cancel()
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
	registration := &alarmRegistration{cancel: alarmCancel}
	if !alarmRegistry.register(event.ID, registration) {
		alarmCancel()
		applogger.Info("Scheduled event already armed", "event_id", event.ID)
		return
	}

	go func() {
		defer alarmRegistry.unregister(event.ID, registration)

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
	tx := database.DB.Begin()
	if tx.Error != nil {
		applogger.Error("fireScheduledEvent: failed to begin transaction", "event_id", event.ID, "error", tx.Error)
		return
	}
	defer tx.Rollback()
	updated := tx.Model(&model.ScheduledEvent{}).
		Where("id = ? AND status = ?", event.ID, model.ScheduledEventStatusPending).
		Update("status", model.ScheduledEventStatusTriggered)
	if err := updated.Error; err != nil {
		applogger.Error("fireScheduledEvent: failed to mark as triggered",
			"event_id", event.ID, "error", err)
		return
	}
	if updated.RowsAffected == 0 {
		applogger.Info("fireScheduledEvent: alarm already handled", "scheduled_event_id", event.ID)
		return
	}
	eventID, err := memory.RecordReferencedEventTx(tx, model.EventTypeScheduled, event.ID)
	if err != nil {
		applogger.Error("fireScheduledEvent: failed to record durable Event", "scheduled_event_id", event.ID, "error", err)
		return
	}
	if err := tx.Commit().Error; err != nil {
		applogger.Error("fireScheduledEvent: failed to commit alarm and Event", "scheduled_event_id", event.ID, "error", err)
		return
	}
	refreshMemorySource(model.MemorySourceEvent, eventID)

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
		EventID:   eventID,
		Payload: &eventqueue.ScheduledEventPayload{
			ScheduledEventID:      event.ID,
			Message:               event.Message,
			Action:                event.Action,
			ActionContent:         event.ActionContent,
			ExpressionInstruction: event.ExpressionInstruction,
		},
	})
}

// armScheduledEvent loads a scheduled event by ID and arms it for firing: it
// fires immediately if the trigger time has already passed, otherwise it
// registers a goroutine that waits until the trigger time. Called from the
// runtime event loop when a new alarm is created (EventTypeAlarmCreated).
func armScheduledEvent(eventID int64) {
	event := &model.ScheduledEvent{}
	if err := database.DB.First(event, eventID).Error; err != nil {
		applogger.Error("armScheduledEvent: failed to load scheduled event",
			"event_id", eventID, "error", err)
		return
	}

	if event.Status != model.ScheduledEventStatusPending {
		applogger.Info("armScheduledEvent: event not pending, skipping",
			"event_id", eventID, "status", event.Status)
		return
	}

	// If the trigger time has already passed (edge case: clock skew or delay),
	// fire immediately instead of registering a goroutine.
	if event.TriggerAt.Before(time.Now()) || event.TriggerAt.Equal(time.Now()) {
		applogger.Info("armScheduledEvent: trigger time already passed, firing immediately",
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
