package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/action"
)

// TestPendingAlarmsCanBeReadAndCancelled covers the resource boundary, the
// timer wake-up, and the durable Action effect without calling an LLM.
func TestPendingAlarmsCanBeReadAndCancelled(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/alarms.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.Decision{}, &model.Action{}, &model.ActionEffect{}, &model.ScheduledEvent{}); err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	for _, person := range []model.Person{{ID: 1, Name: "Self", Status: model.PersonStatusActive}, {ID: 2, Name: "Other", Status: model.PersonStatusActive}} {
		if err := db.Create(&person).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Add(time.Hour).Truncate(time.Second)
	alarms := []model.ScheduledEvent{
		{PersonID: 1, TriggerAt: now.Add(time.Hour), Message: "I should check my reply", Action: model.ScheduledEventActionFullPipeline, Status: model.ScheduledEventStatusPending},
		{PersonID: 1, TriggerAt: now, Message: "I should send the prepared reply", Action: model.ScheduledEventActionSendMessage, ActionContent: "A prepared reply", ExpressionInstruction: "Speak softly", SessionID: 7, Status: model.ScheduledEventStatusPending},
		{PersonID: 1, TriggerAt: now.Add(2 * time.Hour), Message: "Already fired", Status: model.ScheduledEventStatusTriggered},
		{PersonID: 2, TriggerAt: now.Add(3 * time.Hour), Message: "Private to another person", Status: model.ScheduledEventStatusPending},
	}
	for i := range alarms {
		if err := db.Create(&alarms[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	read := func(arguments string) []pendingAlarmView {
		t.Helper()
		output, err := executePendingAlarmList(1, arguments)
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Items   []pendingAlarmView `json:"items"`
			HasMore bool               `json:"has_more"`
		}
		if err := json.Unmarshal([]byte(output), &page); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output, "Already fired") || strings.Contains(output, "Private to another person") {
			t.Fatalf("pending list exposed an inaccessible alarm: %s", output)
		}
		return page.Items
	}
	first := read(`{"page_size":1}`)
	if len(first) != 1 || first[0].AlarmID != alarms[1].ID || first[0].ActionContent != "A prepared reply" || first[0].ExpressionInstruction != "Speak softly" {
		t.Fatalf("first pending page = %+v", first)
	}
	second := read(`{"page":2,"page_size":1}`)
	if len(second) != 1 || second[0].AlarmID != alarms[0].ID {
		t.Fatalf("second pending page = %+v", second)
	}
	decision := model.Decision{PersonID: 1}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	chosen := model.Action{DecisionID: decision.ID, Type: model.ActionTypeCancelAlarm, Status: model.ActionStatusInProgress}
	if err := db.Create(&chosen).Error; err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	if !alarmRegistry.register(alarms[1].ID, &alarmRegistration{cancel: stop, personID: 1}) {
		t.Fatal("could not register alarm waiter")
	}
	if err := cancelScheduledAlarm(1, chosen.ID, alarms[1].ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("cancelled alarm waiter was not stopped")
	}
	var cancelled model.ScheduledEvent
	if err := db.First(&cancelled, alarms[1].ID).Error; err != nil || cancelled.Status != model.ScheduledEventStatusCancelled {
		t.Fatalf("cancelled alarm = %+v, err = %v", cancelled, err)
	}
	var storedAction model.Action
	if err := db.First(&storedAction, chosen.ID).Error; err != nil || storedAction.Status != model.ActionStatusEnded {
		t.Fatalf("cancel action = %+v, err = %v", storedAction, err)
	}
	var effects []model.ActionEffect
	if err := db.Where("action_id = ?", chosen.ID).Find(&effects).Error; err != nil || len(effects) != 1 || effects[0].EffectType != model.ActionEffectScheduledEventCancellation || effects[0].EffectID != alarms[1].ID {
		t.Fatalf("cancellation effects = %+v, err = %v", effects, err)
	}
	remaining := read(`{}`)
	if len(remaining) != 1 || remaining[0].AlarmID != alarms[0].ID {
		t.Fatalf("cancelled alarm remained visible: %+v", remaining)
	}
	if err := cancelScheduledAlarm(1, chosen.ID, alarms[1].ID); err == nil {
		t.Fatal("repeated cancellation succeeded")
	}
	if err := cancelScheduledAlarm(2, chosen.ID, alarms[0].ID); err == nil {
		t.Fatal("another person used the cancellation action")
	}
	otherAction := model.Action{DecisionID: decision.ID, Type: model.ActionTypeCancelAlarm, Status: model.ActionStatusInProgress}
	if err := db.Create(&otherAction).Error; err != nil {
		t.Fatal(err)
	}
	if err := cancelScheduledAlarm(1, otherAction.ID, alarms[2].ID); err == nil {
		t.Fatal("already triggered alarm was cancelled")
	}
	var failedEffects []model.ActionEffect
	if err := db.Where("action_id = ?", otherAction.ID).Find(&failedEffects).Error; err != nil || len(failedEffects) != 0 {
		t.Fatalf("failed cancellation recorded an effect: %+v, err = %v", failedEffects, err)
	}
	if actions := filterValidActions([]action.Action{{Type: action.CancelAlarm, CancelAlarmPlan: &action.CancelAlarmPlan{AlarmID: alarms[0].ID}}}, nil, &Situation{Source: SituationSourceInternal}); len(actions) != 1 {
		t.Fatalf("heartbeat could not request alarm cancellation: %+v", actions)
	}
	// A failed effect write must roll the pending-state update back too.
	rollbackAction := model.Action{DecisionID: decision.ID, Type: model.ActionTypeCancelAlarm, Status: model.ActionStatusInProgress}
	if err := db.Create(&rollbackAction).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&model.ActionEffect{}); err != nil {
		t.Fatal(err)
	}
	if err := cancelScheduledAlarm(1, rollbackAction.ID, alarms[0].ID); err == nil {
		t.Fatal("cancellation committed without an Action effect")
	}
	var stillPending model.ScheduledEvent
	if err := db.First(&stillPending, alarms[0].ID).Error; err != nil || stillPending.Status != model.ScheduledEventStatusPending {
		t.Fatalf("failed cancellation changed alarm status: %+v, err = %v", stillPending, err)
	}
}
