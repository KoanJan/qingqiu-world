package dops

import (
	"testing"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestDeleteAgentRemovesEventSources verifies that the term-index rebuild
// cannot encounter occurrences whose message, work or biography was deleted.
func TestDeleteAgentRemovesEventSources(t *testing.T) {
	oldDB := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/delete.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = oldDB })
	if err := db.AutoMigrate(
		&model.Person{}, &model.AgentConfig{}, &model.Session{}, &model.ParticipantSession{},
		&model.Message{}, &model.SpeechRenderHistory{}, &model.Work{}, &model.Interaction{},
		&model.AgentNarrative{}, &model.Summary{}, &model.FocusHandoff{}, &model.ScheduledEvent{},
		&model.AgentObservation{}, &model.EntityProfile{}, &model.AgentBiography{}, &model.AgentVoice{},
		&model.PSDigest{}, &model.Event{}, &model.EventVector{}, &model.AgentEventBuffer{},
		&model.MemoryTerm{}, &model.Decision{}, &model.Action{}, &model.ActionEffect{}, &model.KBAccess{},
	); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Truncate(time.Second)
	for _, row := range []any{
		&model.Person{ID: 1, Name: "deleted agent", Type: model.PersonTypeAI},
		&model.Person{ID: 2, Name: "remaining person", Type: model.PersonTypeHuman},
		&model.AgentConfig{PersonID: 1},
		&model.Session{ID: 10},
		&model.ParticipantSession{SessionID: 10, ParticipantID: 1},
		&model.Message{ID: 11, SessionID: 10, PersonID: 2, Content: "hello", CreatedAt: base},
		&model.Work{ID: 12, SessionID: 10, PersonID: 1, Description: "work", CreatedAt: base},
		&model.AgentBiography{ID: 13, PersonID: 1},
		&model.Event{ID: 21, EventType: model.EventTypeMessage, RefID: 11, CreatedAt: base},
		&model.Event{ID: 22, EventType: model.EventTypeWorkCompleted, RefID: 12, CreatedAt: base},
		&model.Event{ID: 23, EventType: model.EventTypeBiography, RefID: 13, CreatedAt: base},
		&model.Event{ID: 24, EventType: model.EventTypeJinshuReadCompleted, PayloadJSON: "{}", CreatedAt: base},
		&model.AgentObservation{PersonID: 1, EventID: 21},
		&model.EventVector{EventID: 21, Embedding: []byte{1}},
		&model.MemoryTerm{Term: "hello", SourceKind: model.MemorySourceEvent, SourceID: 21, SourceCreatedAt: base},
		&model.Decision{ID: 31, PersonID: 1, EventID: 21, CreatedAt: base},
		&model.Action{ID: 32, DecisionID: 31, CreatedAt: base},
		&model.ActionEffect{ActionID: 32, EffectType: model.ActionEffectMessage, EffectID: 11, CreatedAt: base},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("create %T: %v", row, err)
		}
	}
	if _, err := DeleteAIPersonCascade(1); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"messages", "works", "agent_biographies", "decisions", "actions", "action_effects", "agent_observations", "event_vectors", "memory_terms"} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s still has %d rows: %v", table, count, err)
		}
	}
	var remaining []model.Event
	if err := db.Order("id").Find(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].ID != 24 {
		t.Fatalf("unrelated event changed or orphaned event remained: %+v", remaining)
	}
}
