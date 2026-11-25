package unreleased

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
)

// Start at the immutable release schema, not AutoMigrate's new unique index.
// Equivalent legacy/wrapped copies converge, keeping Session and Action source;
// unrelated Events and recipients are never backfilled by this conversion.
func TestThirdMigrationPreservesPendingDeliveryContext(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "released.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := database.InitializeFreshAt(db, config.AppVersion); err != nil {
		t.Fatal(err)
	}
	old := model.AgentEventBuffer{PersonID: 11, EventType: int(eventqueue.EventTypeWorkCompleted), SessionID: 31, EventID: 51,
		PayloadJSON: `{"WorkID":71,"Guidance":"finish the task","Status":"success"}`}
	withSource := &eventqueue.AgentEvent{Type: eventqueue.EventTypeWorkCompleted, SessionID: 31, EventID: 51,
		TriggerAction: &eventqueue.TriggerAction{Background: "original request", Reason: "requested task"},
		Payload:       &eventqueue.WorkCompletedPayload{WorkID: 71, Guidance: "finish the task", Status: "success"}}
	encoded, err := eventqueue.Serialize(withSource)
	if err != nil {
		t.Fatal(err)
	}
	richer := old
	richer.PayloadJSON = encoded
	other := old
	other.PersonID = 12
	other.EventID = 52
	for _, row := range []model.AgentEventBuffer{old, richer, other} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	unrelated := model.Event{EventType: model.EventTypeWorkCompleted, RefID: 999}
	if err := db.Create(&unrelated).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Apply(db, 3, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	var rows []model.AgentEventBuffer
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].PersonID != 11 || rows[1].PersonID != 12 {
		t.Fatalf("migrated rows=%+v", rows)
	}
	event, err := eventqueue.Deserialize(rows[0])
	if err != nil || event.SessionID != 31 || event.TriggerAction == nil || event.TriggerAction.Reason != "requested task" || event.Payload.(*eventqueue.WorkCompletedPayload).WorkID != 71 {
		t.Fatalf("lost context=%+v err=%v", event, err)
	}
	duplicate := old
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate delivery accepted")
	}
	var version string
	if err := db.Raw("SELECT version FROM db_versions ORDER BY id DESC LIMIT 1").Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version != config.AppVersion {
		t.Fatalf("release version changed to %s", version)
	}
	var n int64
	if err := db.Model(&model.Event{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("conversion manufactured historical Events")
	}
}

// Conflicting historical records cannot be silently discarded; all conversion
// changes roll back so the original fixture can be inspected and retried.
func TestThirdMigrationRollsBackConflictingDuplicates(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "conflict.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := database.InitializeFreshAt(db, config.AppVersion); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{`"first"`, `"different"`} {
		if err := db.Create(&model.AgentEventBuffer{PersonID: 1, EventID: 2, EventType: int(eventqueue.EventTypeSystemNotification), PayloadJSON: message}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := Apply(db, 3, t.TempDir()); err == nil {
		t.Fatal("conflicting deliveries were discarded")
	}
	var n int64
	if err := db.Model(&model.AgentEventBuffer{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("conflict lost rows: %d", n)
	}
	// The failed step must leave the old nonunique index in place.
	if err := db.Create(&model.AgentEventBuffer{PersonID: 1, EventID: 2, EventType: int(eventqueue.EventTypeSystemNotification), PayloadJSON: `"first"`}).Error; err != nil {
		t.Fatal(err)
	}
}
