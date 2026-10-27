package runtime

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/eventqueue"
)

// TestOwnMessagePreservesReadBoundaryAndPausesCognition covers an asynchronous
// reply arriving while an older incoming event and background cognition exist.
func TestOwnMessagePreservesReadBoundaryAndPausesCognition(t *testing.T) {
	old := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/interleaved-messages.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.Session{}, &model.ParticipantSession{},
		&model.Message{}, &model.Event{}, &model.Decision{}, &model.Action{},
		&model.ActionEffect{}, &model.AgentObservation{}, &model.MemoryTerm{}, &model.CognitiveProcess{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old })

	sender := model.Person{Name: "sender", Type: model.PersonTypeAI}
	human := model.Person{Name: "human", Type: model.PersonTypeHuman}
	session := model.Session{}
	for _, row := range []any{&sender, &human, &session} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	participant := model.ParticipantSession{SessionID: session.ID, ParticipantID: sender.ID}
	if err := db.Create(&participant).Error; err != nil {
		t.Fatal(err)
	}
	incoming := model.Message{SessionID: session.ID, PersonID: human.ID, Content: "Earlier incoming message"}
	if err := db.Create(&incoming).Error; err != nil {
		t.Fatal(err)
	}
	trigger := model.Event{EventType: model.EventTypeMessage, RefID: incoming.ID}
	if err := db.Create(&trigger).Error; err != nil {
		t.Fatal(err)
	}
	priorDecision := model.Decision{PersonID: sender.ID}
	if err := db.Create(&priorDecision).Error; err != nil {
		t.Fatal(err)
	}
	action := model.Action{DecisionID: priorDecision.ID, Type: model.ActionTypeChat, Status: model.ActionStatusInProgress}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	process := model.CognitiveProcess{PersonID: sender.ID, Status: model.CognitiveProcessRunning, Epoch: 1}
	if err := db.Create(&process).Error; err != nil {
		t.Fatal(err)
	}
	cognitionCtx, cancel := context.WithCancel(context.Background())
	r := &agentRuntime{agentPersonID: sender.ID, cognitionWake: make(chan struct{}, 1), cognitiveCancel: cancel, cognitiveProcessID: process.ID, cognitiveEpoch: process.Epoch}
	if !r.acquireCognitiveSlot(process.ID) {
		t.Fatal("cognitive slot was not acquired")
	}
	r.commitMessage(&commitRequest{
		actionID: action.ID, sessionID: session.ID, content: "Later outgoing message",
	})
	if cognitionCtx.Err() == nil || r.currentExecutionSlot() != nil {
		t.Fatal("committed self-observation did not pause background cognition")
	}
	if err := db.First(&process, process.ID).Error; err != nil || process.Status != model.CognitiveProcessPaused {
		t.Fatalf("cognitive process was not paused: status=%d err=%v", process.Status, err)
	}

	var saved model.ParticipantSession
	if err := db.First(&saved, participant.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.LastReadMessageID != 0 {
		t.Fatalf("sending crossed unread incoming message: read=%d", saved.LastReadMessageID)
	}
	var messages []model.Message
	if err := db.Where("session_id = ?", session.ID).Order("id").Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].ID != incoming.ID {
		t.Fatalf("outgoing message was not committed after incoming: %+v", messages)
	}

	event := &eventqueue.AgentEvent{Type: eventqueue.EventTypeNewPrivateChatMessage,
		EventID: trigger.ID, SessionID: session.ID,
		Payload: &eventqueue.NewMessagePayload{MessageID: incoming.ID}}
	comp := &comprehendTypes.Comprehension{Chat: &comprehendTypes.ChatComprehension{
		ReadMessageRange: [2]int64{0, messages[1].ID},
		ReadMessageIDs:   []int64{incoming.ID, messages[1].ID},
	}}
	if err := recordComprehendedObservations(sender.ID, event, comp); err != nil {
		t.Fatal(err)
	}
	situation := buildExternalSituation(event, comp, 100, "")
	if accepted, err := persistDecision(sender.ID, situation, &DecisionResult{Accepted: true}); err != nil || !accepted {
		t.Fatalf("comprehended batch was not committed: accepted=%t err=%v", accepted, err)
	}
	if err := db.First(&saved, participant.ID).Error; err != nil || saved.LastReadMessageID != messages[1].ID {
		t.Fatalf("comprehension did not advance read boundary: read=%d err=%v", saved.LastReadMessageID, err)
	}
	var observed int64
	if err := db.Model(&model.AgentObservation{}).Where("person_id = ? AND event_id = ?", sender.ID, trigger.ID).Count(&observed).Error; err != nil || observed != 1 {
		t.Fatalf("incoming event was not observed: count=%d err=%v", observed, err)
	}
}

// TestCommitMessageRejectsNonParticipant keeps an incorrect existing session
// ID from creating a message or any of its causal records.
func TestCommitMessageRejectsNonParticipant(t *testing.T) {
	old := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/wrong-session.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.Session{}, &model.ParticipantSession{},
		&model.Message{}, &model.Event{}, &model.Decision{}, &model.Action{},
		&model.ActionEffect{}, &model.AgentObservation{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old })

	sender := model.Person{Name: "sender", Type: model.PersonTypeAI}
	other := model.Person{Name: "other", Type: model.PersonTypeHuman}
	session := model.Session{}
	for _, row := range []any{&sender, &other, &session} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.ParticipantSession{SessionID: session.ID, ParticipantID: other.ID}).Error; err != nil {
		t.Fatal(err)
	}
	decision := model.Decision{PersonID: sender.ID}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	action := model.Action{DecisionID: decision.ID, Type: model.ActionTypeChat, Status: model.ActionStatusInProgress}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}

	r := &agentRuntime{agentPersonID: sender.ID}
	r.commitMessage(&commitRequest{actionID: action.ID, sessionID: session.ID, content: "wrong destination"})
	for _, table := range []string{"messages", "events", "action_effects", "agent_observations"} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v; rejected delivery persisted data", table, count, err)
		}
	}
	if err := db.First(&action, action.ID).Error; err != nil || action.Status != model.ActionStatusEnded {
		t.Fatalf("failed Chat action status=%d err=%v", action.Status, err)
	}
}
