package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/llm"
)

func stubDecisionLogger(t *testing.T) {
	t.Helper()
	oldInfo, oldWarn, oldError := applogger.Info, applogger.Warn, applogger.Error
	applogger.Info = func(string, ...any) {}
	applogger.Warn = func(string, ...any) {}
	applogger.Error = func(string, ...any) {}
	t.Cleanup(func() {
		applogger.Info, applogger.Warn, applogger.Error = oldInfo, oldWarn, oldError
	})
}

func TestRepairChatPlansFillsMissingPlan(t *testing.T) {
	stubDecisionLogger(t)
	decision := DecisionResult{Actions: []action.Action{{
		Type:       action.Chat,
		Background: "A person asked a question",
		Reason:     "I should answer",
	}}}
	calls := 0
	complete := func(_ context.Context, messages []llm.Message, schema llm.JSONSchemaDefinition) (string, error) {
		calls++
		if schema.Name != "ChatPlan" || !schema.Strict {
			t.Fatalf("unexpected repair schema: %+v", schema)
		}
		if len(messages) != 1 || !strings.Contains(messages[0].Content, "Session 7") || !strings.Contains(messages[0].Content, "I should answer") {
			t.Fatalf("repair prompt lacks the decision context: %+v", messages)
		}
		return `{"guidance":"I should answer the question","session_id":7}`, nil
	}

	repairChatPlans(context.Background(), complete, "Session 7", 2, &decision)
	if calls != 1 {
		t.Fatalf("repair calls = %d, want 1", calls)
	}
	plan := decision.Actions[0].ChatPlan
	if plan == nil || plan.SessionID != 7 || plan.Guidance != "I should answer the question" {
		t.Fatalf("chat plan was not repaired: %+v", plan)
	}
}

func TestRepairChatPlansSkipsValidPlan(t *testing.T) {
	stubDecisionLogger(t)
	decision := DecisionResult{Actions: []action.Action{{
		Type:     action.Chat,
		ChatPlan: &action.ChatPlan{Guidance: "I should reply", SessionID: 7},
	}}}
	repairChatPlans(context.Background(), func(context.Context, []llm.Message, llm.JSONSchemaDefinition) (string, error) {
		t.Fatal("valid chat plan should not be repaired")
		return "", nil
	}, "Session 7", 2, &decision)
	if decision.Actions[0].ChatPlan.SessionID != 7 {
		t.Fatal("valid chat plan changed")
	}
}

func TestRepairChatPlansRejectsInvalidTarget(t *testing.T) {
	stubDecisionLogger(t)
	for _, output := range []string{
		`{"guidance":"I should reply","session_id":0}`,
		`{"guidance":"I should reply","session_id":-1}`,
	} {
		t.Run(output, func(t *testing.T) {
			decision := DecisionResult{Actions: []action.Action{{Type: action.Chat}}}
			repairChatPlans(context.Background(), func(context.Context, []llm.Message, llm.JSONSchemaDefinition) (string, error) {
				return output, nil
			}, "Session 7", 2, &decision)
			if decision.Actions[0].ChatPlan != nil {
				t.Fatalf("accepted invalid chat plan: %+v", decision.Actions[0].ChatPlan)
			}
		})
	}
}

func TestChatPlanSchemaRequiresDeliveryTarget(t *testing.T) {
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(llm.GenerateSchema[action.ChatPlan](), &schema); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"guidance", "session_id"} {
		found := false
		for _, required := range schema.Required {
			if required == field {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("chat plan schema does not require %q: %v", field, schema.Required)
		}
	}
}
