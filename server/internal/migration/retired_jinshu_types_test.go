package migration

import (
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestMigrateRetiredJinshuTypes checks the old-to-new enum boundary and the
// one-time marker; a second startup must not shift already converted rows.
func TestMigrateRetiredJinshuTypes(t *testing.T) {
	if model.ActionTypeListReceivedJinshu != 7 || model.ActionTypeWaitForExecutionSlot != 11 ||
		model.EventTypeJinshuListed != 7 || model.EventTypeSystemNotification != 12 ||
		eventqueue.EventTypeJinshuListed != 9 || eventqueue.EventTypeSystemNotification != 3 {
		t.Fatal("migration mappings no longer match the runtime enums")
	}
	previous := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "retired.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	if err := db.AutoMigrate(&model.Action{}, &model.AgentEventBuffer{}, &model.Event{}, &model.DBVersion{}); err != nil {
		t.Fatal(err)
	}
	for _, oldType := range []int{6, 7, 8, 9, 10, 11, 12} {
		if err := db.Create(&model.Action{ID: int64(oldType + 1), DecisionID: 1, Type: model.ActionType(oldType), Status: model.ActionStatusInProgress}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, oldType := range []int{8, 9, 10, 15} {
		if err := db.Create(&model.AgentEventBuffer{ID: int64(oldType + 1), PersonID: 1, EventType: oldType, PayloadJSON: "{}"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, oldType := range []int{6, 7, 8, 12} {
		if err := db.Create(&model.Event{ID: int64(oldType + 101), EventType: model.EventType(oldType), PayloadJSON: "{}"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrateRetiredJinshuTypes(); err != nil {
			t.Fatal(err)
		}
		if err := migrateDurableEventTypes(); err != nil {
			t.Fatal(err)
		}
	}
	for oldType, want := range map[int]int{6: 6, 7: -1, 8: 7, 9: 8, 10: 9, 11: 10, 12: 11} {
		var row model.Action
		if err := db.First(&row, oldType+1).Error; err != nil {
			t.Fatal(err)
		}
		if int(row.Type) != want {
			t.Errorf("old Action type %d became %d, want %d", oldType, row.Type, want)
		}
		if oldType == 7 && row.Status != model.ActionStatusEnded {
			t.Errorf("retired Action stayed in progress: %d", row.Status)
		}
	}
	for oldType, want := range map[int]int{8: 8, 10: 9, 15: 14} {
		var row model.AgentEventBuffer
		if err := db.First(&row, oldType+1).Error; err != nil {
			t.Fatal(err)
		}
		if row.EventType != want {
			t.Errorf("old buffered Event type %d became %d, want %d", oldType, row.EventType, want)
		}
	}
	for oldType, want := range map[int]int{6: 6, 7: -1, 8: 7, 12: 11} {
		var row model.Event
		if err := db.First(&row, oldType+101).Error; err != nil {
			t.Fatal(err)
		}
		if int(row.EventType) != want || row.PayloadJSON != "{}" {
			t.Errorf("old durable Event type %d became %+v, want type %d with its payload", oldType, row, want)
		}
	}
	var dropped int64
	if err := db.Model(&model.AgentEventBuffer{}).Where("id = ?", 10).Count(&dropped).Error; err != nil || dropped != 0 {
		t.Fatalf("obsolete buffered result remains: count=%d err=%v", dropped, err)
	}
	var markers int64
	if err := db.Model(&model.DBVersion{}).Where("description = ?", retiredJinshuTypeMigration).Count(&markers).Error; err != nil || markers != 1 {
		t.Fatalf("one-time marker count=%d err=%v", markers, err)
	}
	if err := db.Model(&model.DBVersion{}).Where("description = ?", durableEventTypeMigration).Count(&markers).Error; err != nil || markers != 1 {
		t.Fatalf("durable Event marker count=%d err=%v", markers, err)
	}
}

// TestFreshDatabaseDoesNotShiftNewActionTypesOnRestart protects the path that
// starts at 0.1.19 without any legacy rows to convert.
func TestFreshDatabaseDoesNotShiftNewActionTypesOnRestart(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	config.Init()
	previous := database.DB
	database.Init()
	t.Cleanup(func() { database.DB = previous })

	Run()
	row := model.Action{DecisionID: 1, Type: model.ActionTypeListReceivedJinshu, Status: model.ActionStatusEnded}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	event := model.Event{EventType: model.EventTypeSystemNotification, PayloadJSON: `"hello"`}
	if err := database.DB.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	Run()
	var stored model.Action
	if err := database.DB.First(&stored, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Type != model.ActionTypeListReceivedJinshu {
		t.Fatalf("fresh Action type shifted on restart: %d", stored.Type)
	}
	var storedEvent model.Event
	if err := database.DB.First(&storedEvent, event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedEvent.EventType != model.EventTypeSystemNotification {
		t.Fatalf("fresh Event type shifted on restart: %d", storedEvent.EventType)
	}
}
