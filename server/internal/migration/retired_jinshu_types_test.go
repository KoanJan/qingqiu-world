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

// TestMigrateRetiredJinshuTypes checks enum conversion in the 0.1.19 SQL
// against the columns that existed before that release.
func TestMigrateRetiredJinshuTypes(t *testing.T) {
	if model.ActionTypeListReceivedJinshu != 7 || model.ActionTypeWaitForExecutionSlot != 11 ||
		model.EventTypeJinshuListed != 7 || model.EventTypeSystemNotification != 12 ||
		eventqueue.EventTypeJinshuListed != 9 || eventqueue.EventTypeSystemNotification != 3 {
		t.Fatal("migration mappings no longer match the runtime enums")
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "retired.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{
		`CREATE TABLE db_versions (id INTEGER PRIMARY KEY, version TEXT NOT NULL, description TEXT NOT NULL, applied_at DATETIME NOT NULL)`,
		`CREATE TABLE persons (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE sessions (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE works (id INTEGER PRIMARY KEY, session_id INTEGER NOT NULL)`,
		`CREATE TABLE interactions (id INTEGER PRIMARY KEY, session_id INTEGER NOT NULL)`,
		`CREATE TABLE actions (id INTEGER PRIMARY KEY, type INTEGER NOT NULL, status INTEGER NOT NULL)`,
		`CREATE TABLE agent_event_buffers (id INTEGER PRIMARY KEY, event_type INTEGER NOT NULL)`,
		`CREATE TABLE events (id INTEGER PRIMARY KEY, event_type INTEGER NOT NULL, payload_json TEXT NOT NULL)`,
	} {
		if err := db.Exec(schema).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, oldType := range []int{6, 7, 8, 9, 10, 11, 12} {
		if err := db.Exec(`INSERT INTO actions (id, type, status) VALUES (?, ?, 1)`, oldType+1, oldType).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, oldType := range []int{8, 9, 10, 15} {
		if err := db.Exec(`INSERT INTO agent_event_buffers (id, event_type) VALUES (?, ?)`, oldType+1, oldType).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, oldType := range []int{6, 7, 8, 12} {
		if err := db.Exec(`INSERT INTO events (id, event_type, payload_json) VALUES (?, ?, '{}')`, oldType+101, oldType).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return applyReleaseSQL(tx, "0.1.19") }); err != nil {
		t.Fatal(err)
	}
	for oldType, want := range map[int]int{6: 6, 7: -1, 8: 7, 9: 8, 10: 9, 11: 10, 12: 11} {
		var row struct{ Type, Status int }
		if err := db.Raw(`SELECT type, status FROM actions WHERE id = ?`, oldType+1).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.Type != want {
			t.Errorf("old Action type %d became %d, want %d", oldType, row.Type, want)
		}
		if oldType == 7 && row.Status != 2 {
			t.Errorf("retired Action stayed in progress: %d", row.Status)
		}
	}
	for oldType, want := range map[int]int{8: 8, 10: 9, 15: 14} {
		var eventType int
		if err := db.Raw(`SELECT event_type FROM agent_event_buffers WHERE id = ?`, oldType+1).Scan(&eventType).Error; err != nil {
			t.Fatal(err)
		}
		if eventType != want {
			t.Errorf("old buffered Event type %d became %d, want %d", oldType, eventType, want)
		}
	}
	for oldType, want := range map[int]int{6: 6, 7: -1, 8: 7, 12: 11} {
		var row struct {
			EventType   int
			PayloadJSON string
		}
		if err := db.Raw(`SELECT event_type, payload_json FROM events WHERE id = ?`, oldType+101).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.EventType != want || row.PayloadJSON != "{}" {
			t.Errorf("old durable Event type %d became %+v, want type %d with its payload", oldType, row, want)
		}
	}
	var dropped int64
	if err := db.Raw(`SELECT COUNT(*) FROM agent_event_buffers WHERE id = 10`).Scan(&dropped).Error; err != nil || dropped != 0 {
		t.Fatalf("obsolete buffered result remains: count=%d err=%v", dropped, err)
	}
	var markers int64
	if err := db.Raw(`SELECT COUNT(*) FROM db_versions WHERE description = '0.1.19 compact retired Jinshu Action and buffered Event types'`).Scan(&markers).Error; err != nil || markers != 1 {
		t.Fatalf("one-time marker count=%d err=%v", markers, err)
	}
	if err := db.Raw(`SELECT COUNT(*) FROM db_versions WHERE description = '0.1.19 compact durable Event types and reuse system notifications'`).Scan(&markers).Error; err != nil || markers != 1 {
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
