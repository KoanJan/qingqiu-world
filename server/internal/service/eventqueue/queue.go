// Package eventqueue owns durable Agent deliveries and their bounded read cache.
// Producers persist recipient rows in their Event transaction, then notify by
// Person ID. Runtime consumes one event through Ready and Next; no ACK is used.
package eventqueue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	batchLimit = 64
	// recoveryInterval also bounds retries after database or payload errors.
	recoveryInterval = 30 * time.Second
)

// ErrStopped identifies a subscription that no longer accepts consumption.
var ErrStopped = errors.New("event subscription stopped")

// Subscription has one serial Next consumer. Producers only signal it; they
// never access its batch. The stop channel is closed once; ready is never closed.
type Subscription struct {
	personID      int64
	ready         chan struct{}
	stopped       chan struct{}
	cancel        context.CancelFunc
	batch         []model.AgentEventBuffer
	index         int
	position      int64
	lastDelivery  *model.AgentEventBuffer // Latest removed row, solely for explicit consumer retention.
	paused        bool
	alarmPosition int64
	checkAlarms   atomic.Bool
}

// Ready exposes coalesced opportunities to call Next, including stale signals.
func (s *Subscription) Ready() <-chan struct{} { return s.ready }

// SetPaused suspends normal delivery when Runtime cannot perceive events.
// Control notifications remain accessible. Only the Next consumer calls this.
func (s *Subscription) SetPaused(paused bool) { s.paused = paused }

func (s *Subscription) signal() {
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// Next deletes a normal delivery's buffer row before returning the decoded event.
// AlarmCreated controls are read from pending scheduled records without a buffer
// row to delete. Empty returns (nil, nil); Next never waits for a future event.
// A committed deletion is not undone if the process exits before handling it.
func (s *Subscription) Next(ctx context.Context) (*AgentEvent, error) {
	select {
	case <-s.stopped:
		return nil, ErrStopped
	default:
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.checkAlarms.Swap(false) {
		// Scheduled records are the durable source of control-only registration.
		// The cursor belongs to one scan round. Later rounds revisit pending
		// alarms whose Runtime registration failed; registration is idempotent.
		// Never manufacture an Event or Observation here.
		var alarms []model.ScheduledEvent
		err := database.DB.WithContext(ctx).Where("person_id = ? AND status = ? AND id > ?", s.personID, model.ScheduledEventStatusPending, s.alarmPosition).Order("id ASC").Limit(1).Find(&alarms).Error
		if err != nil {
			s.checkAlarms.Store(true)
			return nil, fmt.Errorf("read alarm registrations for person %d: %w", s.personID, err)
		}
		if len(alarms) > 0 {
			alarm := alarms[0]
			s.alarmPosition = alarm.ID
			s.checkAlarms.Store(true)
			s.signal()
			return &AgentEvent{Type: EventTypeAlarmCreated, SessionID: alarm.SessionID, Payload: &AlarmCreatedPayload{ScheduledEventID: alarm.ID}}, nil
		}
		// End this round without self-signalling. A later notification or tick
		// starts again from the first pending alarm, rather than skipping failures.
		s.alarmPosition = 0
	}
	if s.paused {
		return nil, nil
	}
	if s.index == len(s.batch) {
		var rows []model.AgentEventBuffer
		if err := database.DB.WithContext(ctx).Where("person_id = ? AND id > ?", s.personID, s.position).Order("id ASC").Limit(batchLimit).Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("read deliveries for person %d: %w", s.personID, err)
		}
		if len(rows) == 0 {
			return nil, nil
		}
		s.batch, s.index = rows, 0
	}
	row := s.batch[s.index]
	event, err := Deserialize(row)
	if err != nil {
		return nil, fmt.Errorf("decode delivery %d for person %d: %w", row.ID, s.personID, err)
	}
	if err := database.DB.WithContext(ctx).Where("person_id = ? AND id = ?", s.personID, row.ID).Delete(&model.AgentEventBuffer{}).Error; err != nil {
		return nil, fmt.Errorf("remove delivery %d for person %d: %w", row.ID, s.personID, err)
	}
	s.batch[s.index] = model.AgentEventBuffer{}
	s.index++
	s.position = row.ID
	s.lastDelivery = &row
	s.signal()
	return event, nil
}

// Restore retains the latest normal delivery at its original database ID.
// Only the serial Next consumer calls it, before retrieving another event.
// It invalidates the batch after persistence succeeds so the next read keeps
// delivery order. This is explicit retention, not a processing acknowledgement.
func (s *Subscription) Restore(event *AgentEvent) error {
	if event == nil || s.lastDelivery == nil || event.EventID != s.lastDelivery.EventID {
		return fmt.Errorf("restore requires the latest delivery for person %d", s.personID)
	}
	row := *s.lastDelivery
	// A producer may have enqueued the same Person/Event again since Next
	// removed it. Restore the original ID and context without a second copy.
	if err := database.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "person_id"}, {Name: "event_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"id", "event_type", "session_id", "payload_json", "created_at", "updated_at",
		}),
	}).Create(&row).Error; err != nil {
		return fmt.Errorf("restore delivery %d for person %d: %w", row.ID, s.personID, err)
	}
	s.batch = nil
	s.index = 0
	s.position = 0
	return nil
}

// AppendTx joins the producer's transaction. It never notifies before commit.
// Uniqueness applies while a delivery is pending, not after it is removed.
func AppendTx(tx *gorm.DB, event *AgentEvent, personIDs ...int64) error {
	if event == nil || event.EventID <= 0 || event.Type == EventTypeAlarmCreated {
		return fmt.Errorf("enqueue requires a durable Event")
	}
	encoded, err := Serialize(event)
	if err != nil {
		return err
	}
	for _, personID := range personIDs {
		if personID <= 0 {
			return fmt.Errorf("invalid delivery recipient %d", personID)
		}
		row := model.AgentEventBuffer{PersonID: personID, EventType: int(event.Type), SessionID: event.SessionID, EventID: event.EventID, PayloadJSON: encoded}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "person_id"}, {Name: "event_id"}}, DoNothing: true}).Create(&row).Error; err != nil {
			return fmt.Errorf("enqueue event %d for person %d: %w", event.EventID, personID, err)
		}
	}
	return nil
}

type eventQueue struct {
	mu            sync.RWMutex
	subscriptions map[int64]*Subscription
}

var global = newEventQueue()

// Init starts with a new registry, releasing subscriptions from a previous run.
func Init() {
	global.mu.Lock()
	for id, s := range global.subscriptions {
		s.cancel()
		delete(global.subscriptions, id)
	}
	global.mu.Unlock()
	applogger.Info("Global event queue initialized")
}

func newEventQueue() *eventQueue { return &eventQueue{subscriptions: make(map[int64]*Subscription)} }

// Subscribe returns one subscription per Person; its Runtime is the sole consumer.
func Subscribe(personID int64) *Subscription { return global.Subscribe(personID) }

func (q *eventQueue) Subscribe(personID int64) *Subscription {
	q.mu.Lock()
	defer q.mu.Unlock()
	if s := q.subscriptions[personID]; s != nil {
		return s
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Subscription{personID: personID, ready: make(chan struct{}, 1), stopped: make(chan struct{}), cancel: cancel}
	s.checkAlarms.Store(true)
	q.subscriptions[personID] = s
	s.signal()
	go func() {
		defer close(s.stopped)
		ticker := time.NewTicker(recoveryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.checkAlarms.Store(true)
				s.signal()
			}
		}
	}()
	return s
}

// Unsubscribe removes the subscription. Persistent rows survive shutdown.
func Unsubscribe(personID int64) { global.Unsubscribe(personID) }
func (q *eventQueue) Unsubscribe(personID int64) {
	q.mu.Lock()
	s := q.subscriptions[personID]
	delete(q.subscriptions, personID)
	q.mu.Unlock()
	if s != nil {
		s.cancel()
		<-s.stopped
	}
}

// Notify coalesces wake-ups after the producer's transaction commits.
func Notify(personIDs ...int64) { global.Notify(personIDs...) }
func (q *eventQueue) Notify(personIDs ...int64) {
	for _, id := range personIDs {
		q.mu.RLock()
		s := q.subscriptions[id]
		q.mu.RUnlock()
		if s != nil {
			s.signal()
		}
	}
}

// NotifyAlarmCreated requests a control scan of pending scheduled records.
// A missed notification is repaired by subscription startup or periodic checks.
func NotifyAlarmCreated(personID int64) {
	global.mu.RLock()
	s := global.subscriptions[personID]
	global.mu.RUnlock()
	if s != nil {
		s.checkAlarms.Store(true)
		s.signal()
	}
}
