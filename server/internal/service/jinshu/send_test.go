package jinshu

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
)

// Exercise the real file-delivery producer without a subscribed Runtime. The
// intended recipient and file list must persist, and failed enqueue must leave
// neither a new Event nor a partial delivery. No model calls are involved.
func TestSendPersistsRecipientWithEvent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	config.Init()
	db, err := gorm.Open(sqlite.Open(filepath.Join(root, "jinshu.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.Person{}, &model.Jinshu{}, &model.Event{}, &model.AgentEventBuffer{}, &model.MemoryTerm{}, &model.AgentObservation{}); err != nil {
		t.Fatal(err)
	}
	old := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = old; sqlDB.Close() })
	for _, p := range []model.Person{{ID: 1, Name: "Sender", Type: model.PersonTypeHuman}, {ID: 2, Name: "Recipient", Type: model.PersonTypeAI}} {
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(root, "report.txt")
	if err := os.WriteFile(source, []byte("completed report"), 0600); err != nil {
		t.Fatal(err)
	}
	record, err := Send(SendParams{FromPersonID: 1, ToPersonID: 2, Topic: "Report", Description: "Please review", Files: map[string]string{"report.txt": source}})
	if err != nil {
		t.Fatal(err)
	}
	var rows []model.AgentEventBuffer
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].PersonID != 2 {
		t.Fatalf("intended deliveries=%+v", rows)
	}
	event, err := eventqueue.Deserialize(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := event.Payload.(*eventqueue.JinshuReceivedPayload)
	if !ok || payload.JinshuID != record.ID || payload.FromName != "Sender" || payload.Topic != "Report" || !reflect.DeepEqual(payload.Files, []string{"report.txt"}) {
		t.Fatalf("Jinshu delivery lost context: %+v", event)
	}
	var sourceEvent model.Event
	if err := db.First(&sourceEvent, event.EventID).Error; err != nil {
		t.Fatal(err)
	}
	if sourceEvent.EventType != model.EventTypeJinshu || sourceEvent.RefID != record.ID {
		t.Fatalf("Event source=%+v", sourceEvent)
	}
	// Files were already delivered before the recipient became unavailable.
	// Clearing its old pending rows must not suppress a later producer's enqueue.
	if err := db.Model(&model.Person{}).Where("id = ?", record.ToPersonID).
		Update("status", model.PersonStatusDeceased).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("person_id = ?", record.ToPersonID).Delete(&model.AgentEventBuffer{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := notify(record, []string{"report.txt"}); err != nil {
		t.Fatal(err)
	}
	var pending model.AgentEventBuffer
	if err := db.First(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending.PersonID != record.ToPersonID || pending.EventID == sourceEvent.ID {
		t.Fatalf("late notification was not persisted for its recipient: %+v", pending)
	}
	if err := db.Exec(`CREATE TRIGGER reject_delivery BEFORE INSERT ON agent_event_buffers BEGIN SELECT RAISE(ABORT,'delivery failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := notify(record, []string{"report.txt"}); err == nil {
		t.Fatal("failed enqueue reported success")
	}
	for table, want := range map[string]int64{"events": 2, "agent_event_buffers": 1, "agent_observations": 0} {
		var n int64
		if err := db.Table(table).Count(&n).Error; err != nil || n != want {
			t.Fatalf("%s rows=%d want=%d err=%v", table, n, want, err)
		}
	}
}
