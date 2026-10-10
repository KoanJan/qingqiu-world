package runtime

import (
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestExecutionWaitNotifiesOnceAfterRealRelease checks that a waiting Action
// remains pending while a loop owns the slot and has one durable wake-up.
func TestExecutionWaitNotifiesOnceAfterRealRelease(t *testing.T) {
	previous := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "slot.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	if err := db.AutoMigrate(&model.Person{}, &model.Decision{}, &model.Action{}, &model.ActionEffect{}, &model.Event{}, &model.AgentEventBuffer{}, &model.MemoryTerm{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Person{ID: 9, Name: "waiting", Type: model.PersonTypeAI}).Error; err != nil {
		t.Fatal(err)
	}
	decision := model.Decision{PersonID: 9}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	wait := model.Action{DecisionID: decision.ID, Type: model.ActionTypeWaitForExecutionSlot, Status: model.ActionStatusInProgress, PlanJSON: `{"intention":"Review the next task"}`}
	if err := db.Create(&wait).Error; err != nil {
		t.Fatal(err)
	}
	eventqueue.Init()
	r := &agentRuntime{agentPersonID: 9, agentConfigID: 99}
	if !r.acquireExecutionSlot(41, false) {
		t.Fatal("slot not acquired")
	}
	r.registerExecutionWait(wait.ID)
	var count int64
	if err := db.Model(&model.Event{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("premature event count=%d err=%v", count, err)
	}
	r.releaseExecutionSlot(41, false)
	r.notifyWaitingActions()
	if err := db.Model(&model.Event{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("availability event count=%d err=%v", count, err)
	}
	if err := db.Model(&model.ActionEffect{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("effect count=%d err=%v", count, err)
	}
	if err := db.Model(&model.AgentEventBuffer{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("buffer count=%d err=%v", count, err)
	}
	if err := db.First(&wait, wait.ID).Error; err != nil || wait.Status != model.ActionStatusEnded {
		t.Fatalf("wait status=%d err=%v", wait.Status, err)
	}
}
