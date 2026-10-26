package chat

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/memory"
)

// TestUnreadBatchAndLocalContextHaveSeparateBounds verifies that an unread
// message batch is complete even when the same-session context is bounded.
func TestUnreadBatchAndLocalContextHaveSeparateBounds(t *testing.T) {
	old := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/batch.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.Session{}, &model.ParticipantSession{}, &model.Message{},
		&model.Event{}, &model.AgentObservation{}, &model.Summary{}, &model.AgentNarrative{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old })
	for _, person := range []model.Person{{ID: 1, Name: "Agent", Type: model.PersonTypeAI}, {ID: 2, Name: "Peer", Type: model.PersonTypeHuman}} {
		if err := db.Create(&person).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, sessionID := range []int64{10, 20} {
		if err := db.Create(&model.Session{ID: sessionID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.ParticipantSession{SessionID: 10, ParticipantID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	var currentIDs []int64
	var firstCurrentID, lastCurrentID int64
	for i := 0; i < 66; i++ {
		message := model.Message{SessionID: 10, PersonID: 2, Content: fmt.Sprintf("batch-entry-%02d", i), CreatedAt: base.Add(time.Duration(i) * time.Second)}
		if err := db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID, CreatedAt: message.CreatedAt}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := db.Create(&model.AgentObservation{PersonID: 1, EventID: event.ID}).Error; err != nil {
				t.Fatal(err)
			}
			firstCurrentID = message.ID
		} else {
			currentIDs = append(currentIDs, message.ID)
			lastCurrentID = message.ID
		}
		if i == 30 {
			other := model.Message{SessionID: 20, PersonID: 2, Content: "other-session-only"}
			if err := db.Create(&other).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Create(&model.Summary{SessionID: 10, Version: 1, Content: "observed opening"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentNarrative{SessionID: 10, PersonID: 1, SummaryVersion: 1, Content: "observed opening"}).Error; err != nil {
		t.Fatal(err)
	}
	batch, err := dops.ListMessagesInRange(10, firstCurrentID, lastCurrentID)
	if err != nil || len(batch) != 65 {
		t.Fatalf("unread batch was truncated or crossed sessions: count=%d err=%v", len(batch), err)
	}
	description := formatMessageRange(batch, 1)
	if !strings.Contains(description, "batch-entry-01") || !strings.Contains(description, "batch-entry-65") || strings.Contains(description, "other-session-only") {
		t.Fatalf("batch description lost an endpoint or crossed sessions: %s", description)
	}
	if !strings.Contains(description, "Chat messages from session (session_id=10)") || !strings.Contains(description, "— Peer said:") {
		t.Fatalf("batch attributed the session ID to a speaker: %s", description)
	}
	ownDescription := formatMessageRange([]model.Message{{SessionID: 10, PersonID: 1, Content: "我知道"}}, 1)
	if !strings.Contains(ownDescription, "Chat messages from session (session_id=10)") || !strings.Contains(ownDescription, "— You said: \"我知道\".") {
		t.Fatalf("own speech was not separated from its session: %s", ownDescription)
	}
	window, _, _, err := memory.LoadObservedSessionContext(1, 10, lastCurrentID, 50, currentIDs)
	if err != nil || len(window) != 50 || window[0].Content != "batch-entry-16" || window[49].Content != "batch-entry-65" {
		t.Fatalf("local context did not use the bounded same-session window: count=%d first=%+v err=%v", len(window), window[0], err)
	}
	if hidden, err := memory.ListObservedMessages(1, 10, lastCurrentID, 50, nil); err != nil || len(hidden) != 1 || hidden[0].Content != "batch-entry-00" {
		t.Fatalf("unobserved current batch was admitted as older history: %+v err=%v", hidden, err)
	}
}

func TestMessageSpeakerNameUsesObserverPerspective(t *testing.T) {
	names := map[int64]string{1: "粒粒", 2: "Patrick"}
	if got := messageSpeakerName(names, 1, 1); got != "You" {
		t.Fatalf("own message speaker = %q", got)
	}
	if got := messageSpeakerName(names, 2, 1); got != "Patrick" {
		t.Fatalf("peer message speaker = %q", got)
	}
}
