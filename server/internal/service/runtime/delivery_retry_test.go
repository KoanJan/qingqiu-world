package runtime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/eventqueue"
)

// A post-retrieval energy failure retains the original delivery ahead of its
// cached successors. Only known energy depletion sets the sleeping marker.
func TestPostRetrievalEnergyFailureRestoresDeliveryOrder(t *testing.T) {
	for _, queryFailure := range []bool{true, false} {
		t.Run(fmt.Sprintf("query_failure=%t", queryFailure), func(t *testing.T) {
			db := deliveryDB(t)
			if err := db.AutoMigrate(&model.AgentState{}); err != nil {
				t.Fatal(err)
			}
			queue := eventqueue.Subscribe(2)
			t.Cleanup(func() { eventqueue.Unsubscribe(2) })
			runtime := &agentRuntime{agentPersonID: 2, queue: queue}
			for _, id := range []int64{31, 32} {
				if err := eventqueue.AppendTx(db, &eventqueue.AgentEvent{
					Type: eventqueue.EventTypeSystemNotification, EventID: id, SessionID: 9,
					Payload:       "An event awaiting admission",
					TriggerAction: &eventqueue.TriggerAction{Background: "Original context", Reason: "Original reason"},
				}, 2); err != nil {
					t.Fatal(err)
				}
			}
			var original model.AgentEventBuffer
			if err := db.Where("person_id = ? AND event_id = ?", 2, 31).First(&original).Error; err != nil {
				t.Fatal(err)
			}
			// Mirror Run's first admission check before Next deletes the row.
			state, err := energy.RecoverEnergy(2)
			if err != nil || state.Energy < int(energy.CostPassive) {
				t.Fatalf("initial energy=%+v %v", state, err)
			}
			queue.SetPaused(false)
			event, err := queue.Next(context.Background())
			if err != nil || event == nil || event.EventID != 31 {
				t.Fatalf("retrieve=%+v %v", event, err)
			}
			var count int64
			if err := db.Model(&model.AgentEventBuffer{}).Where("id = ?", original.ID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("Next did not remove original: count=%d err=%v", count, err)
			}
			if queryFailure {
				failNext := true
				if err := db.Callback().Query().Before("gorm:query").Register("fail_post_retrieval_energy", func(tx *gorm.DB) {
					if tx.Statement.Table == "agent_states" && failNext {
						failNext = false
						tx.AddError(fmt.Errorf("transient energy read failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			} else if err := db.Model(&model.AgentState{}).Where("person_id = ?", 2).Update("energy", 0).Error; err != nil {
				t.Fatal(err)
			}
			if runtime.handleEvent(context.Background(), event) {
				t.Fatal("failed energy admission was treated as handled")
			}
			var restored model.AgentEventBuffer
			if err := db.Where("person_id = ? AND event_id = ?", 2, 31).First(&restored).Error; err != nil {
				t.Fatal(err)
			}
			if restored.ID != original.ID || restored.PayloadJSON != original.PayloadJSON || restored.SessionID != original.SessionID {
				t.Fatalf("retention changed delivery identity or context: %+v", restored)
			}
			if err := db.Where("person_id = ?", 2).First(state).Error; err != nil {
				t.Fatal(err)
			}
			if (state.SleepSince == "") != queryFailure {
				t.Fatalf("sleep state does not distinguish query failure from depletion: %+v", state)
			}
			for _, id := range []int64{31, 32} {
				event, err := queue.Next(context.Background())
				if err != nil || event == nil || event.EventID != id {
					t.Fatalf("retry order: event=%+v err=%v want=%d", event, err, id)
				}
			}
		})
	}
}

// Failed Runtime registration must be retried by a later scan in the same
// subscription; already armed alarms retain their existing waiter.
func TestPendingAlarmRegistrationRetriesAfterReadFailure(t *testing.T) {
	db := deliveryDB(t)
	queue := eventqueue.Subscribe(2)
	t.Cleanup(func() { eventqueue.Unsubscribe(2) })
	t.Cleanup(CancelAlarms)
	runtime := &agentRuntime{agentPersonID: 2, queue: queue}
	alarm := model.ScheduledEvent{PersonID: 2, Status: model.ScheduledEventStatusPending, TriggerAt: time.Now().Add(time.Hour)}
	if err := db.Create(&alarm).Error; err != nil {
		t.Fatal(err)
	}
	event, err := queue.Next(context.Background())
	if err != nil || event == nil || event.Type != eventqueue.EventTypeAlarmCreated {
		t.Fatalf("control=%+v %v", event, err)
	}
	failNext := true
	if err := db.Callback().Query().Before("gorm:query").Register("fail_alarm_registration_read", func(tx *gorm.DB) {
		if tx.Statement.Table == "scheduled_events" && failNext {
			failNext = false
			tx.AddError(fmt.Errorf("transient registration read failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	runtime.handleEvent(context.Background(), event)
	alarmRegistry.mu.Lock()
	armed := alarmRegistry.alarms[alarm.ID]
	alarmRegistry.mu.Unlock()
	if armed != nil {
		t.Fatal("failed registration created a timer")
	}
	if event, err := queue.Next(context.Background()); err != nil || event != nil {
		t.Fatalf("scan round did not finish: %+v %v", event, err)
	}
	// Clear the prior success signal and verify round completion did not
	// immediately start a rescan, which would spin on an unregistrable alarm.
	select {
	case <-queue.Ready():
	default:
	}
	if event, err := queue.Next(context.Background()); err != nil || event != nil {
		t.Fatalf("control immediately retried: %+v %v", event, err)
	}
	select {
	case <-queue.Ready():
		t.Fatal("completed alarm scan immediately signalled itself")
	default:
	}
	for i := 0; i < 2; i++ {
		// Public notification requests the same scan as the periodic queue check.
		eventqueue.NotifyAlarmCreated(2)
		event, err := queue.Next(context.Background())
		if err != nil || event == nil || event.Type != eventqueue.EventTypeAlarmCreated {
			t.Fatalf("retry control=%+v %v", event, err)
		}
		runtime.handleEvent(context.Background(), event)
		alarmRegistry.mu.Lock()
		current := alarmRegistry.alarms[alarm.ID]
		alarmRegistry.mu.Unlock()
		if current == nil || (armed != nil && current != armed) {
			t.Fatal("retry failed to arm alarm or replaced its existing waiter")
		}
		armed = current
		if event, err := queue.Next(context.Background()); err != nil || event != nil {
			t.Fatalf("retry round did not finish: %+v %v", event, err)
		}
	}
	if err := db.First(&alarm, alarm.ID).Error; err != nil || alarm.Status != model.ScheduledEventStatusPending {
		t.Fatalf("registration changed scheduled status: %+v %v", alarm, err)
	}
}
