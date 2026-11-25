package eventqueue

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// queueDB uses real SQLite with the product's single-connection boundary.
// Tests inspect durable rows and query counts, rather than simulated transport.
func queueDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "queue.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.Event{}, &model.AgentEventBuffer{}, &model.ScheduledEvent{}, &model.AgentObservation{}); err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous; sqlDB.Close() })
	return db
}

func delivery(eventID int64) *AgentEvent {
	return &AgentEvent{Type: EventTypeWorkCompleted, EventID: eventID, SessionID: 19,
		TriggerAction: &TriggerAction{Background: "original situation", Reason: "requested work"},
		Payload:       &WorkCompletedPayload{WorkID: eventID, Guidance: "complete requested task", Status: "abandoned", CancelActionID: 81, CancelReason: "user cancelled"}}
}

func subscribeTest(t *testing.T, personID int64) (*eventQueue, *Subscription) {
	t.Helper()
	q := newEventQueue()
	s := q.Subscribe(personID)
	t.Cleanup(func() { q.Unsubscribe(personID) })
	return q, s
}

// A recipient-write failure must roll back both the Event and earlier recipients.
func TestEnqueueSharesEventTransactionAndDeduplicatesPendingRecipients(t *testing.T) {
	db := queueDB(t)
	if err := db.Exec(`CREATE TRIGGER reject_recipient BEFORE INSERT ON agent_event_buffers WHEN NEW.person_id = 2 BEGIN SELECT RAISE(ABORT, 'recipient failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		e := model.Event{EventType: model.EventTypeWorkCompleted, RefID: 7}
		if err := tx.Create(&e).Error; err != nil {
			return err
		}
		return AppendTx(tx, delivery(e.ID), 1, 2)
	})
	if err == nil {
		t.Fatal("recipient failure committed")
	}
	for _, table := range []string{"events", "agent_event_buffers", "agent_observations"} {
		var n int64
		if err := db.Table(table).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("%s rows=%d err=%v", table, n, err)
		}
	}
	if err := db.Exec("DROP TRIGGER reject_recipient").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		e := model.Event{EventType: model.EventTypeWorkCompleted, RefID: 7}
		if err := tx.Create(&e).Error; err != nil {
			return err
		}
		if err := AppendTx(tx, delivery(e.ID), 1, 1, 2); err != nil {
			return err
		}
		return AppendTx(tx, delivery(e.ID), 1, 2)
	}); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.Model(&model.AgentEventBuffer{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pending rows=%d", n)
	}
	if err := db.Model(&model.AgentObservation{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("enqueue manufactured observations")
	}
}

// Loading a batch must not consume it; each Next removes only its own row.
// Query counting detects accidental per-event payload fetches or cache reloads.
func TestBatchRetrievalDeletesIndividuallyAndPreservesOrder(t *testing.T) {
	db := queueDB(t)
	for i := 0; i < batchLimit+2; i++ {
		if err := AppendTx(db, delivery(int64(i+1)), 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := AppendTx(db, delivery(9999), 2); err != nil {
		t.Fatal(err)
	}
	q, s := subscribeTest(t, 1)
	var reads, deletes int
	if err := db.Callback().Query().After("gorm:query").Register("count_delivery_reads", func(tx *gorm.DB) {
		if tx.Statement.Table == "agent_event_buffers" {
			reads++
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Delete().After("gorm:delete").Register("count_delivery_deletes", func(tx *gorm.DB) {
		if tx.Statement.Table == "agent_event_buffers" {
			deletes++
		}
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < batchLimit; i++ {
		select {
		case <-s.Ready():
		case <-time.After(time.Second):
			t.Fatal("cached events lost readiness")
		}
		e, err := s.Next(context.Background())
		if err != nil || e == nil || e.EventID != int64(i+1) {
			t.Fatalf("item %d: %+v err=%v", i, e, err)
		}
		if reads != 1 || deletes != i+1 {
			t.Fatalf("reads=%d deletes=%d at %d", reads, deletes, i)
		}
		if i == 0 {
			if err := AppendTx(db, delivery(10000), 1); err != nil {
				t.Fatal(err)
			}
			for j := 0; j < 100; j++ {
				q.Notify(1)
			}
			if len(s.batch) != batchLimit {
				t.Fatal("producer changed cache")
			}
			// Inspect with raw SQL so payload-query counting remains meaningful.
			var remaining int64
			if err := db.Raw("SELECT COUNT(*) FROM agent_event_buffers WHERE person_id=1").Scan(&remaining).Error; err != nil {
				t.Fatal(err)
			}
			if remaining != batchLimit+2 {
				t.Fatalf("batch load removed future rows: %d", remaining)
			}
		}
	}
	for _, id := range []int64{batchLimit + 1, batchLimit + 2, 10000} {
		e, err := s.Next(context.Background())
		if err != nil || e == nil || e.EventID != id {
			t.Fatalf("remaining %+v err=%v want=%d", e, err, id)
		}
	}
	if reads != 2 || deletes != batchLimit+3 {
		t.Fatalf("reads=%d deletes=%d", reads, deletes)
	}
	e, err := s.Next(context.Background())
	if err != nil || e != nil {
		t.Fatalf("empty=%+v err=%v", e, err)
	}
	q.Unsubscribe(1)
	s = q.Subscribe(1)
	e, err = s.Next(context.Background())
	if err != nil || e != nil {
		t.Fatalf("removed delivery replayed: %+v %v", e, err)
	}
	other := q.Subscribe(2)
	defer q.Unsubscribe(2)
	e, err = other.Next(context.Background())
	if err != nil || e == nil || e.EventID != 9999 {
		t.Fatalf("other recipient lost: %+v %v", e, err)
	}
}

// An actual SQLite deletion error must retain the slot and permit a later retry.
func TestNextFailurePreservesCurrentDelivery(t *testing.T) {
	db := queueDB(t)
	_, s := subscribeTest(t, 1)
	if err := AppendTx(db, delivery(17), 1); err != nil {
		t.Fatal(err)
	}
	<-s.Ready()
	if err := db.Exec(`CREATE TRIGGER reject_delete BEFORE DELETE ON agent_event_buffers BEGIN SELECT RAISE(ABORT, 'delete failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	e, err := s.Next(context.Background())
	if err == nil || e != nil || s.position != 0 || s.index != 0 {
		t.Fatalf("failed deletion advanced: %+v %v", e, err)
	}
	select {
	case <-s.Ready():
		t.Fatal("failure immediately signalled itself")
	default:
	}
	if err := db.Exec("DROP TRIGGER reject_delete").Error; err != nil {
		t.Fatal(err)
	}
	e, err = s.Next(context.Background())
	if err != nil || e == nil || e.EventID != 17 {
		t.Fatalf("retry=%+v %v", e, err)
	}
	payload := e.Payload.(*WorkCompletedPayload)
	if payload.CancelActionID != 81 || payload.CancelReason != "user cancelled" || e.TriggerAction.Reason != "requested work" || e.SessionID != 19 {
		t.Fatal("typed payload or provenance lost")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e, err = s.Next(ctx)
	if e != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%+v %v", e, err)
	}
}

// Explicit retention must restore the old ID and reload a populated cache,
// without replaying earlier consumed rows or moving the current event to the tail.
func TestRestoreKeepsOriginalDeliveryOrder(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending_duplicate=%t", duplicate), func(t *testing.T) {
			db := queueDB(t)
			_, s := subscribeTest(t, 1)
			for _, id := range []int64{10, 20, 30} {
				if err := AppendTx(db, delivery(id), 1); err != nil {
					t.Fatal(err)
				}
			}
			var original model.AgentEventBuffer
			if err := db.Where("person_id = ? AND event_id = ?", 1, 20).First(&original).Error; err != nil {
				t.Fatal(err)
			}
			for _, id := range []int64{10, 20} {
				event, err := s.Next(context.Background())
				if err != nil || event == nil || event.EventID != id {
					t.Fatalf("initial event=%+v err=%v want=%d", event, err, id)
				}
				if id != 20 {
					continue
				}
				if duplicate {
					if err := AppendTx(db, delivery(20), 1); err != nil {
						t.Fatal(err)
					}
				}
				if err := AppendTx(db, delivery(40), 1); err != nil {
					t.Fatal(err)
				}
				// Retention can be reached again during shutdown; it is idempotent.
				for i := 0; i < 2; i++ {
					if err := s.Restore(event); err != nil {
						t.Fatal(err)
					}
				}
			}
			var restored model.AgentEventBuffer
			if err := db.Where("person_id = ? AND event_id = ?", 1, 20).First(&restored).Error; err != nil {
				t.Fatal(err)
			}
			if restored.ID != original.ID || restored.PayloadJSON != original.PayloadJSON || restored.SessionID != original.SessionID || !restored.CreatedAt.Equal(original.CreatedAt) {
				t.Fatalf("restored delivery changed identity or context: %+v", restored)
			}
			for _, id := range []int64{20, 30, 40} {
				event, err := s.Next(context.Background())
				if err != nil || event == nil || event.EventID != id {
					t.Fatalf("restored order: event=%+v err=%v want=%d", event, err, id)
				}
			}
			if event, err := s.Next(context.Background()); err != nil || event != nil {
				t.Fatalf("consumed or duplicate row replayed: %+v %v", event, err)
			}
		})
	}
}

// A failed restore must not discard the cache before its database row exists.
func TestRestoreFailureKeepsCacheUntilPersistenceSucceeds(t *testing.T) {
	db := queueDB(t)
	_, s := subscribeTest(t, 1)
	for _, id := range []int64{10, 20} {
		if err := AppendTx(db, delivery(id), 1); err != nil {
			t.Fatal(err)
		}
	}
	event, err := s.Next(context.Background())
	if err != nil || event == nil {
		t.Fatalf("retrieve=%+v %v", event, err)
	}
	index, position, batchSize := s.index, s.position, len(s.batch)
	if err := db.Exec(`CREATE TRIGGER reject_restore BEFORE INSERT ON agent_event_buffers BEGIN SELECT RAISE(ABORT, 'restore failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(event); err == nil {
		t.Fatal("restore failure reported success")
	}
	if s.index != index || s.position != position || len(s.batch) != batchSize {
		t.Fatal("failed restore invalidated consumption state")
	}
	if err := db.Exec("DROP TRIGGER reject_restore").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(event); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{10, 20} {
		event, err := s.Next(context.Background())
		if err != nil || event == nil || event.EventID != id {
			t.Fatalf("retry order: event=%+v err=%v want=%d", event, err, id)
		}
	}
}

func TestDecodeAndQueryErrorsAreNotEmptyOrDestructive(t *testing.T) {
	db := queueDB(t)
	q, s := subscribeTest(t, 1)
	if err := AppendTx(db, delivery(18), 1); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AgentEventBuffer{}).Where("person_id=1").Update("payload_json", "{").Error; err != nil {
		t.Fatal(err)
	}
	<-s.Ready()
	e, err := s.Next(context.Background())
	if err == nil || e != nil || s.index != 0 || s.position != 0 {
		t.Fatalf("decode failure=%+v %v", e, err)
	}
	var n int64
	if err := db.Model(&model.AgentEventBuffer{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("invalid row silently deleted")
	}
	q.Unsubscribe(1)
	s = q.Subscribe(1)
	if err := db.Callback().Query().Before("gorm:query").Register("fail_delivery_query", func(tx *gorm.DB) {
		if tx.Statement.Table == "agent_event_buffers" {
			tx.AddError(fmt.Errorf("injected read failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	e, err = s.Next(context.Background())
	if err == nil || e != nil || s.position != 0 {
		t.Fatalf("query failure=%+v %v", e, err)
	}
}

// Alarm control uses its durable source and remains accessible while delivery
// is paused for energy. It creates neither an Event nor an Observation.
func TestAlarmControlRecoveryAndPausedDelivery(t *testing.T) {
	db := queueDB(t)
	_, s := subscribeTest(t, 1)
	s.SetPaused(true)
	alarm := model.ScheduledEvent{PersonID: 1, Status: model.ScheduledEventStatusPending, TriggerAt: time.Now().Add(time.Hour)}
	if err := db.Create(&alarm).Error; err != nil {
		t.Fatal(err)
	}
	if err := AppendTx(db, delivery(4), 1); err != nil {
		t.Fatal(err)
	}
	e, err := s.Next(context.Background())
	if err != nil || e == nil || e.Type != EventTypeAlarmCreated || e.Payload.(*AlarmCreatedPayload).ScheduledEventID != alarm.ID {
		t.Fatalf("control=%+v %v", e, err)
	}
	e, err = s.Next(context.Background())
	if err != nil || e != nil {
		t.Fatalf("paused normal event=%+v %v", e, err)
	}
	var n int64
	if err := db.Model(&model.AgentEventBuffer{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("paused delivery consumed")
	}
	s.SetPaused(false)
	e, err = s.Next(context.Background())
	if err != nil || e == nil || e.EventID != 4 {
		t.Fatalf("resume=%+v %v", e, err)
	}
	if err := db.Model(&model.Event{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("alarm control created Event")
	}
	if err := db.Model(&model.AgentObservation{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("alarm control created Observation")
	}
}

func TestConcurrentNotifyAndSubscriptionRemoval(t *testing.T) {
	queueDB(t)
	q, s := subscribeTest(t, 1)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				q.Notify(1)
			}
		}()
	}
	for i := 0; i < 30; i++ {
		q.Unsubscribe(1)
		q.Subscribe(1)
	}
	wg.Wait()
	q.Unsubscribe(1)
	e, err := s.Next(context.Background())
	if e != nil || !errors.Is(err, ErrStopped) {
		t.Fatalf("stopped=%+v %v", e, err)
	}
}

// A producer crash after commit must not require another business notification.
// This exercises the real periodic worker and its production recovery interval.
func TestPeriodicCheckFindsCommittedDeliveryWithoutNotification(t *testing.T) {
	db := queueDB(t)
	_, s := subscribeTest(t, 1)
	s.SetPaused(true)
	<-s.Ready()
	if e, err := s.Next(context.Background()); e != nil || err != nil {
		t.Fatalf("initial queue=%+v %v", e, err)
	}
	if err := AppendTx(db, delivery(44), 1); err != nil {
		t.Fatal(err)
	}
	alarm := model.ScheduledEvent{PersonID: 1, Status: model.ScheduledEventStatusPending, TriggerAt: time.Now().Add(time.Hour)}
	if err := db.Create(&alarm).Error; err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Ready():
	case <-time.After(recoveryInterval + 5*time.Second):
		t.Fatal("periodic recovery never signalled")
	}
	e, err := s.Next(context.Background())
	if err != nil || e == nil || e.Type != EventTypeAlarmCreated || e.Payload.(*AlarmCreatedPayload).ScheduledEventID != alarm.ID {
		t.Fatalf("periodic control=%+v %v", e, err)
	}
	s.SetPaused(false)
	e, err = s.Next(context.Background())
	if err != nil || e == nil || e.EventID != 44 {
		t.Fatalf("periodic delivery=%+v %v", e, err)
	}
}

// Queue order follows recipient-row insertion rather than the Event IDs.
func TestDeliveryOrderUsesBufferID(t *testing.T) {
	db := queueDB(t)
	_, s := subscribeTest(t, 1)
	for _, id := range []int64{200, 5} {
		if err := AppendTx(db, delivery(id), 1); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{200, 5} {
		event, err := s.Next(context.Background())
		if err != nil || event == nil || event.EventID != id {
			t.Fatalf("want event %d, got %+v %v", id, event, err)
		}
	}
}

// After startup's empty control scan, new alarms must become readable through
// the public notification path immediately, including during an energy pause.
func TestAlarmCreatedNotificationWakesPausedSubscription(t *testing.T) {
	db := queueDB(t)
	q, s := subscribeTest(t, 1)
	previous := global
	global = q
	t.Cleanup(func() { global = previous })
	s.SetPaused(true)
	<-s.Ready()
	if event, err := s.Next(context.Background()); err != nil || event != nil {
		t.Fatalf("initial control scan=%+v %v", event, err)
	}
	alarm := model.ScheduledEvent{PersonID: 1, Status: model.ScheduledEventStatusPending, TriggerAt: time.Now().Add(time.Hour)}
	if err := db.Create(&alarm).Error; err != nil {
		t.Fatal(err)
	}
	NotifyAlarmCreated(1)
	select {
	case <-s.Ready():
	default:
		t.Fatal("alarm creation did not wake paused subscription")
	}
	event, err := s.Next(context.Background())
	if err != nil || event == nil || event.Type != EventTypeAlarmCreated || event.Payload.(*AlarmCreatedPayload).ScheduledEventID != alarm.ID {
		t.Fatalf("new control=%+v %v", event, err)
	}
	// Checking again must not register the same pending alarm repeatedly.
	if event, err := s.Next(context.Background()); err != nil || event != nil {
		t.Fatalf("duplicate registration=%+v %v", event, err)
	}
}
