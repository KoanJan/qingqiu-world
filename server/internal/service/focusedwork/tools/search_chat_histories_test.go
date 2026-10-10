package tools

import (
	"path/filepath"
	"strings"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestSearchChatHistoriesRequiresObservation keeps historical participation
// from exposing messages that the agent never actually received.
func TestSearchChatHistoriesRequiresObservation(t *testing.T) {
	previous := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "history.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	if err := db.AutoMigrate(&model.ParticipantSession{}, &model.Message{}, &model.Event{}, &model.AgentObservation{}, &model.Person{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ParticipantSession{SessionID: 4, ParticipantID: 7}).Error; err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"Visible history", "Unobserved history"} {
		message := model.Message{SessionID: 4, PersonID: 8, Content: content}
		if err := db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		if content == "Visible history" {
			if err := db.Create(&model.AgentObservation{PersonID: 7, EventID: event.ID}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	result, err := NewSearchChatHistoriesTool(7).Execute(map[string]interface{}{"query": "history"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Visible history") || strings.Contains(result, "Unobserved history") {
		t.Fatalf("search crossed the observation boundary: %s", result)
	}
}
