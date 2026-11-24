package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/eventqueue"
)

func deliveryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "producer.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.Person{}, &model.Session{}, &model.ParticipantSession{}, &model.Message{}, &model.Event{}, &model.AgentEventBuffer{}, &model.AgentObservation{}, &model.ScheduledEvent{}, &model.Decision{}, &model.Action{}, &model.ActionEffect{}, &model.MemoryTerm{}); err != nil {
		t.Fatal(err)
	}
	old := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = old; sqlDB.Close() })
	for _, p := range []model.Person{{ID: 1, Name: "Human", Type: model.PersonTypeHuman}, {ID: 2, Name: "Agent A", Type: model.PersonTypeAI}, {ID: 3, Name: "Agent B", Type: model.PersonTypeAI}, {ID: 4, Name: "Deceased", Type: model.PersonTypeAI, Status: model.PersonStatusDeceased}} {
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.Decision{ID: 6, PersonID: 2}).Error; err != nil {
		t.Fatal(err)
	}

	return db
}

// Producers must persist the complete recipient set with the Event, including
// when no AgentConfig/Runtime exists. A later-recipient failure rolls back all.
func TestUserMessagePersistsAtomicIntendedRecipients(t *testing.T) {
	db := deliveryDB(t)
	if err := db.Create(&model.Session{ID: 9}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2, 3, 4} {
		if err := db.Create(&model.ParticipantSession{SessionID: 9, ParticipantID: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.Message{ID: 10, SessionID: 9, PersonID: 1, Content: "Hello"}).Error; err != nil {
		t.Fatal(err)
	}
	SendNewMessageEvent(9, 10, 1, "Hello", "Human")
	var rows []model.AgentEventBuffer
	if err := db.Order("person_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].PersonID != 2 || rows[1].PersonID != 3 || rows[0].EventID != rows[1].EventID {
		t.Fatalf("recipients=%+v", rows)
	}
	if err := db.Exec(`CREATE TRIGGER fail_recipient BEFORE INSERT ON agent_event_buffers WHEN NEW.person_id=3 BEGIN SELECT RAISE(ABORT,'fail recipient'); END`).Error; err != nil {
		t.Fatal(err)
	}
	SendNewMessageEvent(9, 10, 1, "Hello", "Human")
	var n int64
	if err := db.Model(&model.Event{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("failed enqueue left Event count=%d", n)
	}
	if err := db.Model(&model.AgentEventBuffer{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("failed enqueue left partial recipients=%d", n)
	}
	if err := db.Model(&model.AgentObservation{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("message enqueue created Observation")
	}
}

// Alarm firing must roll back its triggered marker if delivery insertion fails;
// retry commits status, Event, and buffer together without creating duplicates.
func TestAlarmFiringCommitsStatusEventAndDeliveryTogether(t *testing.T) {
	db := deliveryDB(t)
	alarm := model.ScheduledEvent{PersonID: 2, SessionID: 9, Message: "Reminder", TriggerAt: time.Now(), Status: model.ScheduledEventStatusPending, Action: model.ScheduledEventActionSendMessage, ActionContent: "Prepared message"}
	if err := db.Create(&alarm).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_delivery BEFORE INSERT ON agent_event_buffers BEGIN SELECT RAISE(ABORT,'fail delivery'); END`).Error; err != nil {
		t.Fatal(err)
	}
	fireScheduledEvent(&alarm)
	var saved model.ScheduledEvent
	if err := db.First(&saved, alarm.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != model.ScheduledEventStatusPending {
		t.Fatal("failed delivery marked alarm triggered")
	}
	var n int64
	if err := db.Model(&model.Event{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("failed delivery left alarm Event")
	}
	if err := db.Exec("DROP TRIGGER fail_delivery").Error; err != nil {
		t.Fatal(err)
	}
	fireScheduledEvent(&alarm)
	fireScheduledEvent(&alarm)
	if err := db.First(&saved, alarm.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Status != model.ScheduledEventStatusTriggered {
		t.Fatal("alarm did not trigger")
	}
	var rows []model.AgentEventBuffer
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("alarm deliveries=%d", len(rows))
	}
	event, err := eventqueue.Deserialize(rows[0])
	if err != nil || event.SessionID != 9 || event.Payload.(*eventqueue.ScheduledEventPayload).ActionContent != "Prepared message" {
		t.Fatalf("alarm context=%+v %v", event, err)
	}
	if err := db.Model(&model.Event{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("alarm Event count=%d", n)
	}
}

// A failed result enqueue must not leave an ended Action without its Event or
// intended self-delivery. The successful path retains Action cognitive context.
func TestSelfHeldResultIncludesDeliveryInActionTransaction(t *testing.T) {
	db := deliveryDB(t)
	a := model.Action{ID: 5, DecisionID: 6, Status: model.ActionStatusInProgress}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	r := &agentRuntime{agentPersonID: 2}
	trigger := &eventqueue.TriggerAction{Background: "request", Reason: "self-held result"}
	if err := db.Exec(`CREATE TRIGGER fail_result BEFORE INSERT ON agent_event_buffers BEGIN SELECT RAISE(ABORT,'fail result'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.emitSelfHeldResult(eventqueue.EventTypeSystemNotification, model.EventTypeSystemNotification, 9, "result", a.ID, trigger); err == nil {
		t.Fatal("failed insertion reported success")
	}
	if err := db.First(&a, a.ID).Error; err != nil {
		t.Fatal(err)
	}
	if a.Status != model.ActionStatusInProgress {
		t.Fatal("failed result ended Action")
	}
	for _, table := range []string{"events", "action_effects", "agent_event_buffers"} {
		var n int64
		if err := db.Table(table).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s rows after failure=%d", table, n)
		}
	}
	if err := db.Exec("DROP TRIGGER fail_result").Error; err != nil {
		t.Fatal(err)
	}
	if err := r.emitSelfHeldResult(eventqueue.EventTypeSystemNotification, model.EventTypeSystemNotification, 9, "result", a.ID, trigger); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&a, a.ID).Error; err != nil {
		t.Fatal(err)
	}
	if a.Status != model.ActionStatusEnded {
		t.Fatal("successful result did not end Action")
	}
	var row model.AgentEventBuffer
	if err := db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	event, err := eventqueue.Deserialize(row)
	if err != nil || event.TriggerAction == nil || event.TriggerAction.Reason != trigger.Reason {
		t.Fatalf("result provenance=%+v %v", event, err)
	}
}

// An Agent's outgoing message must atomically create only the other active
// Agents' deliveries. Sender Observation is its existing business effect; a
// delivery failure rolls back Message, Event, effect and Observation together.
func TestAgentMessagePersistsPeerDeliveryAtomically(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "enqueue_failure"
		}
		t.Run(name, func(t *testing.T) {
			db := deliveryDB(t)
			if err := db.Create(&model.Session{ID: 9}).Error; err != nil {
				t.Fatal(err)
			}
			for _, id := range []int64{1, 2, 3, 4} {
				if err := db.Create(&model.ParticipantSession{SessionID: 9, ParticipantID: id}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Create(&model.Action{ID: 5, DecisionID: 6, Type: model.ActionTypeChat, Status: model.ActionStatusInProgress}).Error; err != nil {
				t.Fatal(err)
			}
			sub := eventqueue.Subscribe(3)
			t.Cleanup(func() { eventqueue.Unsubscribe(3) })
			<-sub.Ready()
			if event, err := sub.Next(context.Background()); err != nil || event != nil {
				t.Fatalf("initial queue=%+v %v", event, err)
			}
			if fail {
				if err := db.Exec(`CREATE TRIGGER reject_peer BEFORE INSERT ON agent_event_buffers BEGIN SELECT RAISE(ABORT,'peer delivery failure'); END`).Error; err != nil {
					t.Fatal(err)
				}
			}
			r := &agentRuntime{agentPersonID: 2}
			r.commitMessage(&commitRequest{actionID: 5, sessionID: 9, content: "A response for my peers"})
			want := int64(1)
			if fail {
				want = 0
			}
			for _, table := range []string{"messages", "events", "action_effects", "agent_observations", "agent_event_buffers"} {
				var n int64
				if err := db.Table(table).Count(&n).Error; err != nil || n != want {
					t.Fatalf("%s rows=%d want=%d err=%v", table, n, want, err)
				}
			}
			if fail {
				select {
				case <-sub.Ready():
					t.Fatal("failed transaction notified peer")
				default:
				}
				return
			}
			var row model.AgentEventBuffer
			if err := db.First(&row).Error; err != nil {
				t.Fatal(err)
			}
			if row.PersonID != 3 {
				t.Fatalf("incorrect recipient=%d", row.PersonID)
			}
			select {
			case <-sub.Ready():
			default:
				t.Fatal("committed delivery did not notify peer")
			}
			event, err := sub.Next(context.Background())
			if err != nil || event == nil {
				t.Fatalf("peer retrieval=%+v %v", event, err)
			}
			var message model.Message
			if err := db.First(&message).Error; err != nil {
				t.Fatal(err)
			}
			payload, ok := event.Payload.(*eventqueue.NewMessagePayload)
			if !ok || payload.MessageID != message.ID || payload.MessageContent != message.Content || payload.SpeakerName != "Agent A" || event.SessionID != 9 {
				t.Fatalf("peer payload=%+v", event)
			}
			var observation model.AgentObservation
			if err := db.First(&observation).Error; err != nil {
				t.Fatal(err)
			}
			if observation.PersonID != 2 {
				t.Fatal("enqueue created a recipient Observation")
			}
		})
	}
}

// A reference-backed Action result uses the same atomic enqueue boundary as
// self-held results; an insertion failure cannot leave an ended Action/Event.
func TestReferencedResultPersistsActionAndDeliveryAtomically(t *testing.T) {
	db := deliveryDB(t)
	if err := db.AutoMigrate(&model.Jinshu{}); err != nil {
		t.Fatal(err)
	}
	record := model.Jinshu{FromPersonID: 2, ToPersonID: 1, Topic: "Report"}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	a := model.Action{ID: 5, DecisionID: 6, Type: model.ActionTypeSendJinshu, Status: model.ActionStatusInProgress}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	r := &agentRuntime{agentPersonID: 2}
	payload := &eventqueue.JinshuSentPayload{JinshuID: record.ID, ToName: "Human", Topic: record.Topic, Status: "success"}
	trigger := &eventqueue.TriggerAction{Background: "deliver report", Reason: "requested delivery"}
	emit := func() error {
		return r.emitReferencedResult(eventqueue.EventTypeJinshuSent, model.EventTypeJinshuSent, 0, record.ID, payload, a.ID, model.ActionEffectJinshu, trigger)
	}
	if err := db.Exec(`CREATE TRIGGER reject_result BEFORE INSERT ON agent_event_buffers BEGIN SELECT RAISE(ABORT,'result delivery failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := emit(); err == nil {
		t.Fatal("failed result insertion reported success")
	}
	for _, table := range []string{"events", "action_effects", "agent_event_buffers"} {
		var n int64
		if err := db.Table(table).Count(&n).Error; err != nil || n != 0 {
			t.Fatalf("%s rows=%d err=%v", table, n, err)
		}
	}
	if err := db.First(&a, a.ID).Error; err != nil || a.Status != model.ActionStatusInProgress {
		t.Fatalf("Action after rollback=%+v err=%v", a, err)
	}
	if err := db.Exec("DROP TRIGGER reject_result").Error; err != nil {
		t.Fatal(err)
	}
	if err := emit(); err != nil {
		t.Fatal(err)
	}
	var row model.AgentEventBuffer
	if err := db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	event, err := eventqueue.Deserialize(row)
	if err != nil {
		t.Fatal(err)
	}
	sent, ok := event.Payload.(*eventqueue.JinshuSentPayload)
	if !ok || row.PersonID != 2 || sent.JinshuID != record.ID || sent.Status != "success" || event.TriggerAction == nil || *event.TriggerAction != *trigger {
		t.Fatalf("result context=%+v", event)
	}
	var source model.Event
	if err := db.First(&source, row.EventID).Error; err != nil || source.RefID != record.ID || source.EventType != model.EventTypeJinshuSent {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	if err := db.First(&a, a.ID).Error; err != nil || a.Status != model.ActionStatusEnded {
		t.Fatalf("committed Action=%+v err=%v", a, err)
	}
}

// Biography is produced before a subscriber exists; config ID must not be
// mistaken for recipient Person ID, and no Observation is created by enqueue.
func TestBiographyPersistsDeliveryWithoutSubscriber(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "enqueue_failure"
		}
		t.Run(name, func(t *testing.T) {
			db := deliveryDB(t)
			if err := db.AutoMigrate(&model.AgentBiography{}); err != nil {
				t.Fatal(err)
			}
			if fail {
				if err := db.Exec(`CREATE TRIGGER reject_biography BEFORE INSERT ON agent_event_buffers BEGIN SELECT RAISE(ABORT,'biography delivery failure'); END`).Error; err != nil {
					t.Fatal(err)
				}
			}
			SendBiographyEvent(999, 2)
			want := int64(1)
			if fail {
				want = 0
			}
			for table, nWant := range map[string]int64{"events": want, "agent_event_buffers": want, "agent_observations": 0} {
				var n int64
				if err := db.Table(table).Count(&n).Error; err != nil || n != nWant {
					t.Fatalf("%s rows=%d want=%d err=%v", table, n, nWant, err)
				}
			}
			if fail {
				return
			}
			var row model.AgentEventBuffer
			if err := db.First(&row).Error; err != nil {
				t.Fatal(err)
			}
			event, err := eventqueue.Deserialize(row)
			if err != nil {
				t.Fatal(err)
			}
			var biography model.AgentBiography
			if err := db.First(&biography).Error; err != nil {
				t.Fatal(err)
			}
			payload, ok := event.Payload.(*eventqueue.BiographyPayload)
			if !ok || row.PersonID != 2 || payload.BiographyID != biography.ID || payload.Content != biography.Content {
				t.Fatalf("biography delivery=%+v", event)
			}
		})
	}
}

// Invoke the real Work.Run exit path with cancellation before any model call.
// Work status/handoff may finish independently, but Event and recipient buffer
// must commit together and preserve the originating Action context.
func TestWorkExitPersistsEventAndDeliveryAtomically(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "enqueue_failure"
		}
		t.Run(name, func(t *testing.T) {
			db := deliveryDB(t)
			if err := db.AutoMigrate(&model.Work{}, &model.FocusHandoff{}, &model.KBUsageTrace{}); err != nil {
				t.Fatal(err)
			}
			record := model.Work{PersonID: 2, SessionID: 9, Description: "An interrupted task", Status: model.WorkStatusRunning}
			if err := db.Create(&record).Error; err != nil {
				t.Fatal(err)
			}
			if fail {
				if err := db.Exec(`CREATE TRIGGER reject_work BEFORE INSERT ON agent_event_buffers BEGIN SELECT RAISE(ABORT,'work delivery failure'); END`).Error; err != nil {
					t.Fatal(err)
				}
			}
			trigger := &eventqueue.TriggerAction{Background: "original request", Reason: "requested task"}
			w := &work{ID: record.ID, agent: &agentRuntime{agentPersonID: 2}, sessionID: 9, plan: &action.WorkPlan{Guidance: record.Description}, triggerAction: trigger, done: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			w.Run(ctx)
			select {
			case <-w.done:
			default:
				t.Fatal("Work did not finish")
			}
			want := int64(1)
			if fail {
				want = 0
			}
			for table, nWant := range map[string]int64{"events": want, "agent_event_buffers": want, "agent_observations": 0} {
				var n int64
				if err := db.Table(table).Count(&n).Error; err != nil || n != nWant {
					t.Fatalf("%s rows=%d want=%d err=%v", table, n, nWant, err)
				}
			}
			if fail {
				return
			}
			var row model.AgentEventBuffer
			if err := db.First(&row).Error; err != nil {
				t.Fatal(err)
			}
			event, err := eventqueue.Deserialize(row)
			if err != nil {
				t.Fatal(err)
			}
			payload, ok := event.Payload.(*eventqueue.WorkCompletedPayload)
			if !ok || row.PersonID != 2 || event.SessionID != 9 || payload.WorkID != record.ID || payload.Guidance != record.Description || payload.Status != "abandoned" || event.TriggerAction == nil || *event.TriggerAction != *trigger {
				t.Fatalf("Work delivery context=%+v", event)
			}
		})
	}
}

// A Work already running at death can finish after pending-delivery cleanup.
// Its Event and intended delivery must still persist without a subscriber.
func TestLateWorkResultPersistsDeliveryAfterDeath(t *testing.T) {
	db := deliveryDB(t)
	if err := db.AutoMigrate(&model.Work{}, &model.FocusHandoff{}, &model.KBUsageTrace{}); err != nil {
		t.Fatal(err)
	}
	record := model.Work{PersonID: 2, Description: "already running task", Status: model.WorkStatusRunning}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { _, err := dops.MarkAIPersonDeceasedTx(tx, 2); return err }); err != nil {
		t.Fatal(err)
	}
	w := &work{ID: record.ID, agent: &agentRuntime{agentPersonID: 2}, plan: &action.WorkPlan{Guidance: record.Description}, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Run(ctx)
	var events []model.Event
	if err := db.Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	var pending []model.AgentEventBuffer
	if err := db.Find(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || len(pending) != 1 {
		t.Fatalf("late Work result: Events=%d pending=%d", len(events), len(pending))
	}
	if events[0].EventType != model.EventTypeWorkCompleted || events[0].RefID != record.ID || pending[0].EventID != events[0].ID || pending[0].PersonID != record.PersonID {
		t.Fatalf("late Work delivery lost association: Event=%+v pending=%+v", events[0], pending[0])
	}
}
