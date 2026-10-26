package runtime

import (
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestInterruptedEventBufferPreservesUndecidedDelivery checks that shutdown
// leaves exactly one replayable event and never buffers an accepted decision.
func TestInterruptedEventBufferPreservesUndecidedDelivery(t *testing.T) {
	old := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/shutdown.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Decision{}, &model.AgentEventBuffer{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old })

	runtime := &agentRuntime{agentPersonID: 9}
	event := &eventqueue.AgentEvent{
		Type:      eventqueue.EventTypeNewPrivateChatMessage,
		SessionID: 11,
		EventID:   888,
		Payload: &eventqueue.NewMessagePayload{
			MessageID:      826,
			MessageContent: "A message interrupted by shutdown",
			SpeakerName:    "Sender",
		},
	}
	for i := 0; i < 2; i++ {
		if err := runtime.bufferInterruptedEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	var buffers []model.AgentEventBuffer
	if err := db.Find(&buffers).Error; err != nil {
		t.Fatal(err)
	}
	if len(buffers) != 1 {
		t.Fatalf("expected one replayable event, got %d", len(buffers))
	}
	replayed, err := deserializeBufferedEvent(buffers[0])
	if err != nil || replayed.EventID != event.EventID || replayed.SessionID != event.SessionID {
		t.Fatalf("buffer lost event identity: %+v, %v", replayed, err)
	}
	message, ok := replayed.Payload.(*eventqueue.NewMessagePayload)
	if !ok || message.MessageID != 826 || message.MessageContent != "A message interrupted by shutdown" {
		t.Fatalf("buffer lost message payload: %+v", replayed.Payload)
	}
	if err := db.Create(&model.Decision{PersonID: 9, EventID: 888}).Error; err != nil {
		t.Fatal(err)
	}
	if err := runtime.bufferInterruptedEvent(event); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.AgentEventBuffer{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("accepted event was buffered again: count=%d err=%v", count, err)
	}
}

// TestBufferedCancellationKeepsItsTerminalMeaning ensures a restart cannot
// turn a cancellation result into an ordinary completion decision.
func TestBufferedCancellationKeepsItsTerminalMeaning(t *testing.T) {
	event := &eventqueue.AgentEvent{
		Type: eventqueue.EventTypeWorkCompleted, SessionID: 15, EventID: 900,
		Payload: &eventqueue.WorkCompletedPayload{
			WorkID: 19, Guidance: "Contact Zero", Status: "abandoned",
			CancelActionID: 815, CancelReason: "Patrick asked me to stop",
		},
	}
	encoded, err := serializeEventPayload(event)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := deserializeBufferedEvent(model.AgentEventBuffer{
		EventType: int(event.Type), SessionID: event.SessionID,
		EventID: event.EventID, PayloadJSON: encoded,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := replayed.Payload.(*eventqueue.WorkCompletedPayload)
	if !ok || payload.CancelActionID != 815 || payload.CancelReason != "Patrick asked me to stop" {
		t.Fatalf("replay lost cancellation provenance: %+v", replayed.Payload)
	}
}
