package runtime

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/action"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/eventqueue"
)

// TestPersistDecisionCoordinatesReplayAndReadBoundary checks the crash
// boundary shared by accepted actions and the aggregated chat read position.
func TestPersistDecisionCoordinatesReplayAndReadBoundary(t *testing.T) {
	old := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/decision.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Event{}, &model.Decision{}, &model.Action{}, &model.ParticipantSession{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old })
	participant := model.ParticipantSession{SessionID: 7, ParticipantID: 1, LastReadMessageID: 2}
	if err := db.Create(&participant).Error; err != nil {
		t.Fatal(err)
	}
	source := model.Event{EventType: model.EventTypeMessage, RefID: 20}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	situation := buildExternalSituation(&eventqueue.AgentEvent{
		Type: eventqueue.EventTypeNewPrivateChatMessage, EventID: source.ID, SessionID: 7,
	}, &comprehendTypes.Comprehension{Chat: &comprehendTypes.ChatComprehension{
		ReadMessageRange: [2]int64{2, 9},
	}}, 100, "")
	failed := DecisionResult{}
	if _, err := persistDecision(1, situation, &failed); err == nil {
		t.Fatal("failed Decide was recorded as intentional silence")
	}
	result := DecisionResult{Accepted: true, Actions: []action.Action{{
		Type: action.Chat, ChatPlan: &action.ChatPlan{Guidance: "I should answer", SessionID: 7},
	}}}
	accepted, err := persistDecision(1, situation, &result)
	if err != nil || !accepted || result.Actions[0].ID <= 0 {
		t.Fatalf("accepted decision was not persisted: accepted=%t id=%d err=%v", accepted, result.Actions[0].ID, err)
	}
	if accepted, err := persistDecision(1, situation, &result); err != nil || accepted {
		t.Fatalf("replayed Event accepted twice: accepted=%t err=%v", accepted, err)
	}
	if accepted, err := hasAcceptedDecision(1, source.ID); err != nil || !accepted {
		t.Fatalf("accepted Event not found before replay: accepted=%t err=%v", accepted, err)
	}
	var saved model.ParticipantSession
	if err := db.First(&saved, participant.ID).Error; err != nil || saved.LastReadMessageID != 9 {
		t.Fatalf("read boundary did not commit with decision: read=%d err=%v", saved.LastReadMessageID, err)
	}
	var decisions, actions int64
	db.Model(&model.Decision{}).Count(&decisions)
	db.Model(&model.Action{}).Count(&actions)
	if decisions != 1 || actions != 1 {
		t.Fatalf("unexpected records after replay: decisions=%d actions=%d", decisions, actions)
	}
}

// TestSiblingActionStatusesRemainIndependent verifies that completing one
// action in a Decision neither completes its sibling nor proves an effect.
func TestSiblingActionStatusesRemainIndependent(t *testing.T) {
	old := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/sibling_actions.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Event{}, &model.Decision{}, &model.Action{}, &model.ActionEffect{}, &model.MemoryTerm{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old })
	source := model.Event{EventType: model.EventTypeBiography, PayloadJSON: `{"test":true}`}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	situation := buildExternalSituation(&eventqueue.AgentEvent{Type: eventqueue.EventTypeBiography, EventID: source.ID},
		&comprehendTypes.Comprehension{EventDescription: "two independent messages"}, 100, "")
	result := DecisionResult{Accepted: true, Actions: []action.Action{
		{Type: action.Chat, Background: "request", Reason: "answer", ChatPlan: &action.ChatPlan{Guidance: "answer A", SessionID: 7}},
		{Type: action.Chat, Background: "request", Reason: "contact", ChatPlan: &action.ChatPlan{Guidance: "contact C", SessionID: 8}},
	}}
	if accepted, err := persistDecision(1, situation, &result); err != nil || !accepted {
		t.Fatalf("two sibling actions were not persisted: accepted=%t err=%v", accepted, err)
	}
	firstID, secondID := result.Actions[0].ID, result.Actions[1].ID
	if firstID <= 0 || secondID <= 0 || firstID == secondID {
		t.Fatalf("sibling IDs invalid: first=%d second=%d", firstID, secondID)
	}
	if err := endAction(secondID); err != nil {
		t.Fatal(err)
	}
	var first, second model.Action
	if err := db.First(&first, firstID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&second, secondID).Error; err != nil {
		t.Fatal(err)
	}
	if first.Status != model.ActionStatusInProgress || second.Status != model.ActionStatusEnded {
		t.Fatalf("ending one action changed its sibling: first=%d second=%d", first.Status, second.Status)
	}
	var effects int64
	if err := db.Model(&model.ActionEffect{}).Count(&effects).Error; err != nil || effects != 0 {
		t.Fatalf("ended action was mistaken for a successful effect: effects=%d err=%v", effects, err)
	}
	if err := endAction(firstID); err != nil {
		t.Fatal(err)
	}
}
