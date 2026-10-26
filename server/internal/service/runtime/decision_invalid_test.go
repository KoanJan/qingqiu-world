package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	comprehendTypes "qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/eventqueue"
)

// TestAllInvalidActionsDoNotPersistDecision exercises the actual LLM Decide
// path with a fixed local response, then verifies its persistence boundary.
func TestAllInvalidActionsDoNotPersistDecision(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local HTTP listener is unavailable: %v", err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode model request: %v", err)
		}
		if len(request.Messages) != 1 || !strings.Contains(request.Messages[0].Content, "A biography changed.") {
			t.Errorf("current Event was missing from the initial prompt: %+v", request.Messages)
		} else {
			prompt := request.Messages[0].Content
			for _, marker := range []string{"event_id=", "decision_id=", "action_id="} {
				if strings.Contains(prompt, marker) {
					t.Errorf("internal causal ID leaked into initial prompt: %s", marker)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"decide","arguments":"{\"thoughts\":\"reply\",\"actions\":[{\"type\":0,\"background\":\"message\",\"reason\":\"reply\",\"chat_plan\":{\"guidance\":\"answer\",\"session_id\":0}}]}"}}]}}]}`)
	})}}
	server.Start()
	defer server.Close()

	old := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/invalid_decision.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.AgentConfig{}, &model.LLMConfig{}, &model.Event{}, &model.Decision{}, &model.Action{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old })
	const personID int64 = 890001
	for _, row := range []any{
		&model.Person{ID: personID, Name: "Decision validation test", Type: model.PersonTypeAI},
		&model.LLMConfig{ID: 1, Name: "local", ModelID: "fixed", BaseURL: server.URL, APIKey: "test"},
		&model.AgentConfig{ID: 1, PersonID: personID, LLMConfigID: 1},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	source := model.Event{EventType: model.EventTypeBiography, PayloadJSON: `{"content":"test"}`}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	situation := buildExternalSituation(&eventqueue.AgentEvent{Type: eventqueue.EventTypeBiography, EventID: source.ID},
		&comprehendTypes.Comprehension{EventDescription: "A biography changed."}, 100, "")
	situation.generalReady = true
	result := decideWithLLM(context.Background(), situation, personID, nil)
	if result.Accepted || len(result.Actions) != 0 {
		t.Fatalf("invalid action was accepted: %+v", result)
	}
	if _, err := persistDecision(personID, situation, &result); err == nil {
		t.Fatal("failed Decide was persisted as a successful Decision")
	}
	var count int64
	if err := db.Model(&model.Decision{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("unexpected Decision rows: count=%d err=%v", count, err)
	}
}
