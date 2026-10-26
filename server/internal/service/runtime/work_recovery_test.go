package runtime

import (
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestWorkRecoveryDistinguishesInterruptionAndRepairsDelivery verifies both
// crash boundaries without restarting a model-backed runtime.
func TestWorkRecoveryDistinguishesInterruptionAndRepairsDelivery(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("AOS_ROOT", filepath.Join(root, "aos"))
	config.Init()
	previous := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(root, "recovery.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	if err := db.AutoMigrate(&model.Person{}, &model.AgentConfig{}, &model.Work{}, &model.FocusHandoff{},
		&model.Event{}, &model.AgentObservation{}, &model.AgentEventBuffer{}, &model.MemoryTerm{}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&model.Person{ID: 1, Name: "Recovering agent", Type: model.PersonTypeAI},
		&model.AgentConfig{ID: 10, PersonID: 1},
		&model.Work{ID: 20, PersonID: 1, Description: "unfinished", Status: model.WorkStatusRunning},
		&model.Work{ID: 21, PersonID: 1, Description: "finished", Status: model.WorkStatusCompleted},
		&model.FocusHandoff{PersonID: 1, WorkID: 21, Source: model.FocusSourceExternal,
			Status: model.FocusHandoffCompleted, Summary: "finished result"},
		&model.Event{EventType: model.EventTypeWorkCompleted, RefID: 21},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("create %T: %v", row, err)
		}
	}
	for i := 0; i < 2; i++ {
		recoverActiveWorks(10)
	}
	var interrupted model.Work
	if err := db.First(&interrupted, 20).Error; err != nil || interrupted.Status != model.WorkStatusAbandoned || interrupted.FocusPhase != model.FocusPhasePaused {
		t.Fatalf("interrupted Work = %+v, err=%v", interrupted, err)
	}
	var handoff model.FocusHandoff
	if err := db.Where("work_id = ?", 20).Take(&handoff).Error; err != nil || handoff.Status != model.FocusHandoffInterrupted {
		t.Fatalf("interrupted handoff = %+v, err=%v", handoff, err)
	}
	for _, workID := range []int64{20, 21} {
		var events []model.Event
		if err := db.Where("event_type = ? AND ref_id = ?", model.EventTypeWorkCompleted, workID).Find(&events).Error; err != nil || len(events) != 1 {
			t.Fatalf("Work %d events = %+v, err=%v", workID, events, err)
		}
		var buffers []model.AgentEventBuffer
		if err := db.Where("person_id = ? AND event_id = ?", 1, events[0].ID).Find(&buffers).Error; err != nil || len(buffers) != 1 {
			t.Fatalf("Work %d buffers = %+v, err=%v", workID, buffers, err)
		}
	}
}
