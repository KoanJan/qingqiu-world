package memory

import (
	"testing"
	"time"

	"qingqiu-world-server/internal/model"
)

// TestSessionContextUsesOnlyObservedMessages verifies that an unobserved
// message remains private unless it belongs to the current accepted batch.
func TestSessionContextUsesOnlyObservedMessages(t *testing.T) {
	db := recallTestDB(t)
	if err := db.AutoMigrate(&model.Summary{}, &model.AgentNarrative{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ParticipantSession{SessionID: 10, ParticipantID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Now().Truncate(time.Second)
	for id, content := range []string{"原始委托", "对象是表格", "直接转发给我"} {
		message := model.Message{ID: int64(id + 1), SessionID: 10, PersonID: 2, Content: content, CreatedAt: base.Add(time.Duration(id) * time.Minute)}
		if err := db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		event := model.Event{ID: int64(id + 1), EventType: model.EventTypeMessage, RefID: message.ID, CreatedAt: message.CreatedAt}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		if id < 2 {
			if err := db.Create(&model.AgentObservation{PersonID: 1, EventID: event.ID}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Create(&model.Summary{SessionID: 10, Version: 2, Content: "旧对话概要"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentNarrative{SessionID: 10, PersonID: 1, SummaryVersion: 2, Content: "旧对话叙事"}).Error; err != nil {
		t.Fatal(err)
	}

	messages, version, narrative, err := LoadObservedSessionContext(1, 10, 3, 50, nil)
	if err != nil || len(messages) != 2 || version != 2 || narrative != "旧对话叙事" {
		t.Fatalf("committed context = %v, %d, %q, %v", messages, version, narrative, err)
	}
	messages, _, _, err = LoadObservedSessionContext(1, 10, 3, 50, []int64{3})
	if err != nil || len(messages) != 3 || messages[2].Content != "直接转发给我" {
		t.Fatalf("current batch was not admitted: %v, %v", messages, err)
	}
	if err := db.Where("person_id = ? AND event_id = ?", 1, 2).Delete(&model.AgentObservation{}).Error; err != nil {
		t.Fatal(err)
	}
	messages, version, narrative, err = LoadObservedSessionContext(1, 10, 3, 50, []int64{3})
	if err != nil || len(messages) != 2 || version != -1 || narrative != "" {
		t.Fatalf("unobserved summary leaked: %v, %d, %q, %v", messages, version, narrative, err)
	}
}
