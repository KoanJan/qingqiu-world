package privatespace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/llm"
)

// A fixed local provider response exercises the real digest producer and its
// SQLite transaction, not model quality. Event and recipient rows must either
// commit together or both roll back when enqueue fails.
func TestGeneratedDigestPersistsAtomicDelivery(t *testing.T) {
	const digest = "I finished reviewing the task and will revisit the open question."
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "digest-test", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": digest}, "finish_reason": "stop"}}})
	}))
	defer provider.Close()
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "enqueue_failure"
		}
		t.Run(name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "digest.db")), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			sqlDB.SetMaxOpenConns(1)
			if err := db.AutoMigrate(&model.Person{}, &model.PSDigest{}, &model.FocusHandoff{}, &model.Event{}, &model.AgentEventBuffer{}, &model.ScheduledEvent{}, &model.MemoryTerm{}, &model.AgentObservation{}); err != nil {
				t.Fatal(err)
			}
			old := database.DB
			database.DB = db
			t.Cleanup(func() { database.DB = old; sqlDB.Close() })
			if err := db.Create(&model.Person{ID: 7, Name: "Digest owner", Type: model.PersonTypeAI}).Error; err != nil {
				t.Fatal(err)
			}
			if fail {
				if err := db.Exec(`CREATE TRIGGER reject_delivery BEFORE INSERT ON agent_event_buffers BEGIN SELECT RAISE(ABORT,'delivery failure'); END`).Error; err != nil {
					t.Fatal(err)
				}
			}
			loop := &Loop{personID: 7, llmClient: llm.NewChatModel(provider.URL, "test-key", "test-model"), messages: []llm.Message{{Role: "assistant", Content: "I should review the open question."}}}
			loop.generateDigest()
			var record model.PSDigest
			if err := db.First(&record).Error; err != nil {
				t.Fatal(err)
			}
			if record.PersonID != 7 || record.Digest != digest {
				t.Fatalf("digest record=%+v", record)
			}
			want := int64(1)
			if fail {
				want = 0
			}
			for table, nWant := range map[string]int64{"events": want, "agent_event_buffers": want, "agent_observations": 0} {
				var n int64
				if err := db.Table(table).Count(&n).Error; err != nil || n != nWant {
					t.Fatalf("%s rows=%d want=%d err=%v", table, n, nWant, err)
				}
			}
			if fail {
				return
			}
			subscription := eventqueue.Subscribe(7)
			t.Cleanup(func() { eventqueue.Unsubscribe(7) })
			event, err := subscription.Next(context.Background())
			if err != nil || event == nil {
				t.Fatalf("digest retrieval=%+v %v", event, err)
			}
			payload, ok := event.Payload.(*eventqueue.PSCompletedPayload)
			if !ok || payload.DigestID != record.ID || payload.Digest != digest || event.SessionID != 0 {
				t.Fatalf("digest delivery=%+v", event)
			}
		})
	}
}
