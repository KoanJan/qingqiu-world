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
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/llm"
)

// TestWorkspaceRecallExplainsDeclaredUse verifies that Decide sees a workspace
// and its use as named records, without raw ownership or numeric enum fields.
func TestWorkspaceRecallExplainsDeclaredUse(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/workspace-recall.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Workspace{}, &model.WorkspaceUse{}, &model.Work{},
		&model.ActionEffect{}, &model.Action{}, &model.Decision{}); err != nil {
		t.Fatal(err)
	}
	oldDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = oldDB })
	workspace := model.Workspace{PersonID: 13, RelativePath: "work/brief", Name: "简报", Purpose: "整理资料"}
	if err := db.Create(&workspace).Error; err != nil {
		t.Fatal(err)
	}
	work := model.Work{PersonID: 13, Description: "整理知识库", Status: model.WorkStatusCompleted}
	if err := db.Create(&work).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.WorkspaceUse{WorkspaceID: workspace.ID, SourceType: model.WorkspaceUseWork,
		SourceID: work.ID, Role: model.WorkspaceUseDefault}).Error; err != nil {
		t.Fatal(err)
	}
	output, err := executeWorkspaceRecall(13, recallArguments{WorkspaceID: workspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{`"workspace_id":`, `"name":"简报"`, "You used this workspace as the default workspace", "(work_id=", "currently completed"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("workspace recall omits %q: %s", wanted, output)
		}
	}
	for _, unwanted := range []string{`"person_id":`, `"source_type":`, `"source_id":`, `"role":`, `"work_status":`} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("workspace recall leaked storage field %q: %s", unwanted, output)
		}
	}
}

// TestDecideLoopBoundsNetworkRequests checks the actual tool list sent to an
// OpenAI-compatible endpoint, including the terminal request after recalls.
func TestDecideLoopBoundsNetworkRequests(t *testing.T) {
	requests := 0
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local HTTP listener is unavailable: %v", err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var input struct {
			ToolChoice        string `json:"tool_choice"`
			ParallelToolCalls *bool  `json:"parallel_tool_calls"`
			Tools             []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if requests > maxDecideRequests {
			t.Errorf("unexpected sixth model request")
		}
		if input.ToolChoice != "required" || input.ParallelToolCalls == nil || *input.ParallelToolCalls {
			t.Errorf("request %d did not require one tool call: choice=%q parallel=%v", requests, input.ToolChoice, input.ParallelToolCalls)
		}
		if requests < maxDecideRequests && len(input.Tools) < 2 {
			t.Errorf("request %d lost recall tools", requests)
		}
		if requests == 1 {
			names := make(map[string]bool, len(input.Tools))
			for _, tool := range input.Tools {
				names[tool.Function.Name] = true
			}
			for _, name := range []string{"list_sent_jinshu", "list_received_jinshu", "read_jinshu"} {
				if !names[name] {
					t.Errorf("initial decision request is missing %s", name)
				}
			}
		}
		if requests == maxDecideRequests && (len(input.Tools) != 1 || input.Tools[0].Function.Name != "decide") {
			t.Errorf("terminal request did not expose only decide: %+v", input.Tools)
		}
		if requests > 1 && (len(input.Messages) < 3 || input.Messages[len(input.Messages)-1].Role != "tool") {
			t.Errorf("request %d lost the previous recall result", requests)
		}
		name, arguments := "recall_history", "{}"
		if requests == maxDecideRequests {
			name, arguments = "decide", `{"thoughts":"enough information","actions":[]}`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"test","object":"chat.completion","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_%d","type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, requests, name, arguments)
	})}}
	server.Start()
	defer server.Close()
	client := llm.NewChatModel(server.URL, "test", "test-model")
	result, accepted := runDecideLoop(context.Background(), client, 1, 1, "Current situation")
	if !accepted || len(result.Actions) != 0 || requests != maxDecideRequests {
		t.Fatalf("loop did not finish with one empty decision after %d requests: accepted=%v result=%+v", requests, accepted, result)
	}
}

// TestDecideLoopRejectsInvalidToolResponses verifies that protocol failures
// cannot be accepted as an intentional empty Decision.
func TestDecideLoopRejectsInvalidToolResponses(t *testing.T) {
	for _, spec := range []struct {
		name string
		body string
	}{
		{"plain text", `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"I will respond"}}]}`},
		{"parallel calls", `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"one","type":"function","function":{"name":"decide","arguments":"{\"thoughts\":\"x\",\"actions\":[]}"}},{"id":"two","type":"function","function":{"name":"decide","arguments":"{\"thoughts\":\"x\",\"actions\":[]}"}}]}}]}`},
		{"invalid decide", `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"one","type":"function","function":{"name":"decide","arguments":"{"}}]}}]}`},
	} {
		t.Run(spec.name, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Skipf("local HTTP listener is unavailable: %v", err)
			}
			requests := 0
			server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(spec.body))
			})}}
			server.Start()
			defer server.Close()
			client := llm.NewChatModel(server.URL, "test", "test-model")
			if result, accepted := runDecideLoop(context.Background(), client, 1, 1, "Current situation"); accepted || len(result.Actions) != 0 {
				t.Fatalf("invalid response was accepted: %+v", result)
			}
			if requests != 1 {
				t.Fatalf("invalid response caused %d requests", requests)
			}
		})
	}
}

// TestDecideLoopReportsRecallFailureAndRepeatedCall keeps an unavailable
// source distinct from an empty result and avoids executing a duplicate read.
func TestDecideLoopReportsRecallFailureAndRepeatedCall(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local HTTP listener is unavailable: %v", err)
	}
	requests := 0
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var input struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if requests == 2 && (len(input.Messages) == 0 || !strings.Contains(input.Messages[len(input.Messages)-1].Content, "unavailable source, not evidence of absence")) {
			t.Errorf("recall error was not explained to the model: %+v", input.Messages)
		}
		if requests == 3 && (len(input.Messages) == 0 || !strings.Contains(input.Messages[len(input.Messages)-1].Content, "already made")) {
			t.Errorf("duplicate recall was not identified: %+v", input.Messages)
		}
		name, arguments := "recall_history", `{"query":"x","session_id":1}`
		if requests == 3 {
			name, arguments = "decide", `{"thoughts":"source unavailable","actions":[]}`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_%d","type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, requests, name, arguments)
	})}}
	server.Start()
	defer server.Close()
	client := llm.NewChatModel(server.URL, "test", "test-model")
	result, accepted := runDecideLoop(context.Background(), client, 1, 1, "Current situation")
	if !accepted || len(result.Actions) != 0 || requests != 3 {
		t.Fatalf("recall failure was not followed by one accepted decision: %+v accepted=%t requests=%d", result, accepted, requests)
	}
}

// TestDecideLoopReportsTruncatedPage verifies that an oversized recall page
// cannot silently become an empty history result in the model context.
func TestDecideLoopReportsTruncatedPage(t *testing.T) {
	old := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/large-recall.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.FocusHandoff{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old })
	large := strings.Repeat("material ", 110)
	for i := 0; i < 3; i++ {
		row := model.FocusHandoff{PersonID: 1, Orientation: large, Summary: large,
			ConfirmedFindings: large, ArtifactReferences: large, Unresolved: large, NextStep: large}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local HTTP listener is unavailable: %v", err)
	}
	requests := 0
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var input struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if requests == 2 && (len(input.Messages) == 0 || !strings.Contains(input.Messages[len(input.Messages)-1].Content, "omitted material is not evidence of absence")) {
			t.Errorf("truncated page was not explained to the model")
		}
		name, arguments := "recall_focus_handoff", `{"page_size":3}`
		if requests == 2 {
			name, arguments = "decide", `{"thoughts":"recall page was too large","actions":[]}`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_%d","type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, requests, name, arguments)
	})}}
	server.Start()
	defer server.Close()
	client := llm.NewChatModel(server.URL, "test", "test-model")
	result, accepted := runDecideLoop(context.Background(), client, 1, 1, "Current situation")
	if !accepted || len(result.Actions) != 0 || requests != 2 {
		t.Fatalf("oversized recall was not handled: %+v accepted=%t requests=%d", result, accepted, requests)
	}
}

// TestDecideLoopTransportFailureDoesNotRetryRequest verifies the physical
// request count for a server error, beyond the loop's logical request limit.
func TestDecideLoopTransportFailureDoesNotRetryRequest(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local HTTP listener is unavailable: %v", err)
	}
	requests := 0
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "temporary server error", http.StatusInternalServerError)
	})}}
	server.Start()
	defer server.Close()
	client := llm.NewChatModel(server.URL, "test", "test-model")
	failed, accepted := runDecideLoop(context.Background(), client, 1, 1, "Current situation")
	if accepted || len(failed.Actions) != 0 {
		t.Fatalf("server failure was accepted: %+v", failed)
	}
	if requests != 1 {
		t.Fatalf("one failed model request produced %d physical HTTP calls", requests)
	}
	old := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/rule-after-failure.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.Event{}, &model.Decision{}, &model.Action{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old })
	if err := db.Create(&model.Person{ID: 1, Name: "Decide test agent", Type: model.PersonTypeAI}).Error; err != nil {
		t.Fatal(err)
	}
	source := model.Event{EventType: model.EventTypePSDigest}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	// The failed result cannot become a silent Decision. The next fixed rule
	// remains usable and persists its own zero-action Decision without a retry.
	situation := &Situation{
		Source: SituationSourceExternal,
		Matter: SituationMatter{Event: &eventqueue.AgentEvent{Type: eventqueue.EventTypePSCompleted, EventID: source.ID}},
	}
	if _, err := persistDecision(1, situation, &failed); err == nil {
		t.Fatal("failed DecideLoop result persisted as an intentional empty Decision")
	}
	rule := Decide(context.Background(), situation, 1, nil)
	if !rule.Accepted || len(rule.Actions) != 0 || requests != 1 {
		t.Fatalf("rule-based Event failed or retried the model after DecideLoop failure: %+v requests=%d", rule, requests)
	}
	if persisted, err := persistDecision(1, situation, &rule); err != nil || !persisted {
		t.Fatalf("rule-based Decision did not persist after model failure: persisted=%t err=%v", persisted, err)
	}
	var decisions, actions int64
	if err := db.Model(&model.Decision{}).Count(&decisions).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Action{}).Count(&actions).Error; err != nil {
		t.Fatal(err)
	}
	if decisions != 1 || actions != 0 {
		t.Fatalf("wrong records after failed model and fixed rule: decisions=%d actions=%d", decisions, actions)
	}
}

// TestDecideLoopReportsCumulativeRecallBudget checks that multiple valid
// pages cannot silently exceed the decision's total recall context budget.
func TestDecideLoopReportsCumulativeRecallBudget(t *testing.T) {
	old := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/paged-recall.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.FocusHandoff{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old })
	large := strings.Repeat("material ", 60)
	for i := 0; i < 8; i++ {
		row := model.FocusHandoff{PersonID: 1, Orientation: large, Summary: large,
			ConfirmedFindings: large, ArtifactReferences: large, Unresolved: large, NextStep: large}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local HTTP listener is unavailable: %v", err)
	}
	requests := 0
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var input struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode request: %v", err)
		}
		name, arguments := "recall_focus_handoff", `{"page_size":2}`
		if requests == maxDecideRequests {
			name, arguments = "decide", `{"thoughts":"recall budget exhausted","actions":[]}`
		} else if requests > 1 {
			last := input.Messages[len(input.Messages)-1].Content
			var page struct {
				NextCursor string `json:"next_cursor"`
			}
			if err := json.Unmarshal([]byte(last), &page); err != nil || page.NextCursor == "" {
				t.Errorf("request %d lost next cursor: %v", requests, err)
			}
			encoded, _ := json.Marshal(map[string]any{"page_size": 2, "cursor": page.NextCursor})
			arguments = string(encoded)
		}
		if requests == maxDecideRequests && !strings.Contains(input.Messages[len(input.Messages)-1].Content, "omitted material is not evidence of absence") {
			t.Error("cumulative budget warning was not shown to the model")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_%d","type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, requests, name, arguments)
	})}}
	server.Start()
	defer server.Close()
	client := llm.NewChatModel(server.URL, "test", "test-model")
	result, accepted := runDecideLoop(context.Background(), client, 1, 1, "Current situation")
	if !accepted || len(result.Actions) != 0 || requests != maxDecideRequests {
		t.Fatalf("paged recall budget was not handled: %+v accepted=%t requests=%d", result, accepted, requests)
	}
}

// TestParseRecallTimeUsesProjectTimezone checks the timestamps seen in live
// recall calls and ensures explicit offsets remain authoritative.
func TestParseRecallTimeUsesProjectTimezone(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct {
		input string
		want  string
	}{
		{"2026-10-06 23:40:00", "2026-10-06T23:40:00+08:00"},
		{"2026-10-06T23:40:00", "2026-10-06T23:40:00+08:00"},
		{"2026-10-06T23:40:00+09:00", "2026-10-06T22:40:00+08:00"},
		{"2026-10-06T23:40:00Z", "2026-10-07T07:40:00+08:00"},
	} {
		got, err := parseRecallTime(spec.input, location)
		if err != nil {
			t.Fatalf("parse %q: %v", spec.input, err)
		}
		if formatted := got.In(location).Format(time.RFC3339); formatted != spec.want {
			t.Errorf("parse %q = %q, want %q", spec.input, formatted, spec.want)
		}
	}
	if _, err := parseRecallTime("2026-10-06", location); err == nil {
		t.Fatal("date-only value unexpectedly accepted")
	}
}

// TestRecallToolArgumentsRejectScopeExpansion keeps invalid navigation fields
// from silently turning a scoped read into a broader history search.
func TestRecallToolArgumentsRejectScopeExpansion(t *testing.T) {
	for _, spec := range []struct {
		name string
		args recallArguments
	}{
		{"recall_history", recallArguments{}},
		{"recall_history", recallArguments{Query: "青丘", SessionID: 1}},
		{"recall_message", recallArguments{DecisionID: 1}},
		{"recall_action", recallArguments{EventID: 1}},
		{"recall_entity_profile", recallArguments{EntityID: 1, EntityType: 1, Query: "青丘"}},
		{"recall_entity_profile", recallArguments{EntityID: 1, EntityType: 1, FromTime: "2026-01-01T00:00:00Z"}},
	} {
		if err := validateRecallArguments(spec.name, spec.args); err == nil {
			t.Fatalf("%s accepted unrelated or unbounded arguments: %+v", spec.name, spec.args)
		}
	}
	for _, spec := range []struct {
		name string
		args recallArguments
	}{
		{"recall_history", recallArguments{FromTime: "2026-01-01T00:00:00Z"}},
		{"recall_work", recallArguments{WorkID: 1}},
		{"recall_entity_profile", recallArguments{EntityID: 1, EntityType: 1}},
	} {
		if err := validateRecallArguments(spec.name, spec.args); err != nil {
			t.Fatalf("%s rejected valid arguments: %v", spec.name, err)
		}
	}
}
