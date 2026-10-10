package runtime

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestDeceasePreservesSharedSessionAndNotifiesKnownPeerOnce covers the
// objective history boundary without running a model or an event loop.
func TestDeceasePreservesSharedSessionAndNotifiesKnownPeerOnce(t *testing.T) {
	previous := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "death.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	if err := db.AutoMigrate(&model.Person{}, &model.AgentConfig{}, &model.Session{}, &model.ParticipantSession{}, &model.Message{}, &model.Jinshu{}, &model.ScheduledEvent{}, &model.Decision{}, &model.Action{}, &model.AgentEventBuffer{}, &model.Event{}, &model.MemoryTerm{}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&model.Person{ID: 1, Name: "Departing", Type: model.PersonTypeAI},
		&model.Person{ID: 2, Name: "Witness", Type: model.PersonTypeAI},
		&model.AgentConfig{ID: 101, PersonID: 1}, &model.AgentConfig{ID: 102, PersonID: 2},
		&model.Session{ID: 61, Title: "Shared"},
		&model.ParticipantSession{SessionID: 61, ParticipantID: 1},
		&model.ParticipantSession{SessionID: 61, ParticipantID: 2},
		&model.Message{ID: 71, SessionID: 61, PersonID: 1, Content: "Earlier conversation"},
		&model.Decision{ID: 81, PersonID: 1},
		&model.Action{ID: 91, DecisionID: 81, Type: model.ActionTypeChat, Status: model.ActionStatusInProgress},
		&model.Action{ID: 92, DecisionID: 81, Type: model.ActionTypeWaitForExecutionSlot, Status: model.ActionStatusInProgress},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("create %T: %v", row, err)
		}
	}
	eventqueue.Init()
	if changed, err := DeceaseAgent(101, 1); err != nil || !changed {
		t.Fatalf("death: changed=%v err=%v", changed, err)
	}
	if changed, err := DeceaseAgent(101, 1); err != nil || changed {
		t.Fatalf("repeat death: changed=%v err=%v", changed, err)
	}
	var person model.Person
	if err := db.First(&person, 1).Error; err != nil || person.Status != model.PersonStatusDeceased {
		t.Fatalf("person = %+v err=%v", person, err)
	}
	var session model.Session
	if err := db.First(&session, 61).Error; err != nil || session.Status != model.SessionStatusActive {
		t.Fatalf("session = %+v err=%v", session, err)
	}
	for table, want := range map[string]int64{"messages": 1, "participant_sessions": 2, "agent_configs": 2, "agent_event_buffers": 1, "events": 1} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, count, want, err)
		}
	}
	var buffer model.AgentEventBuffer
	if err := db.First(&buffer).Error; err != nil || buffer.PersonID != 2 || buffer.EventType != int(eventqueue.EventTypeSystemNotification) {
		t.Fatalf("notice = %+v err=%v", buffer, err)
	}
	var source model.Event
	if err := db.First(&source, buffer.EventID).Error; err != nil || source.EventType != model.EventTypeSystemNotification {
		t.Fatalf("notice source = %+v err=%v", source, err)
	}
	var message string
	if err := json.Unmarshal([]byte(source.PayloadJSON), &message); err != nil || !strings.Contains(message, "Departing has died") {
		t.Fatalf("notice snapshot = %q err=%v", source.PayloadJSON, err)
	}
	replayed, err := deserializeBufferedEvent(buffer)
	if err != nil || replayed.Payload != message || replayed.FormatDescription() != "[System notification] "+message {
		t.Fatalf("system notification replay = %+v err=%v", replayed, err)
	}
	if fallback := (eventqueue.AgentEvent{Type: eventqueue.EventTypeSystemNotification}).FormatDescription(); fallback != "[System notification]" {
		t.Fatalf("system notification fallback = %q", fallback)
	}
	for _, id := range []int64{91, 92} {
		var action model.Action
		if err := db.First(&action, id).Error; err != nil || action.Status != model.ActionStatusEnded {
			t.Fatalf("unfinished action %d remained open: %+v err=%v", id, action, err)
		}
	}
}
