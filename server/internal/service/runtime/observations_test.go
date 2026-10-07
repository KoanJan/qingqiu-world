package runtime

import (
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/eventqueue"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestComprehendedBatchObservesExactMessages records only the noncontiguous
// message IDs admitted by Comprehend and preserves them on replay.
func TestComprehendedBatchObservesExactMessages(t *testing.T) {
	old := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/observations.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Event{}, &model.AgentObservation{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old })
	var ids []int64
	for _, refID := range []int64{10, 20, 30} {
		event := model.Event{EventType: model.EventTypeMessage, RefID: refID}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, event.ID)
	}
	trigger := &eventqueue.AgentEvent{Type: eventqueue.EventTypeNewPrivateChatMessage, EventID: ids[2]}
	comp := &comprehendTypes.Comprehension{Chat: &comprehendTypes.ChatComprehension{ReadMessageIDs: []int64{10, 30}}}
	if err := recordComprehendedObservations(1, trigger, comp); err != nil {
		t.Fatal(err)
	}
	if err := recordComprehendedObservations(1, trigger, comp); err != nil {
		t.Fatalf("replayed batch failed: %v", err)
	}
	var rows []model.AgentObservation
	if err := db.Order("event_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].EventID != ids[0] || rows[1].EventID != ids[2] {
		t.Fatalf("observed range instead of actual batch: %+v", rows)
	}
}
