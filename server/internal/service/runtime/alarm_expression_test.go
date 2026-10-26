package runtime

import (
	"context"
	"testing"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/eventqueue"
)

// TestSendMessageAlarmRequiresExpressionInstruction protects the fast path
// from creating a message that omits the LLM-authored speech instruction.
func TestSendMessageAlarmRequiresExpressionInstruction(t *testing.T) {
	decision := action.Action{AlarmPlan: &action.AlarmPlan{
		TriggerAt:     "2030-01-01 12:00:00",
		Message:       "I should send the prepared reminder.",
		Action:        "send_message",
		ActionContent: "The prepared reminder.",
	}}
	if isValidCreateAlarmAction(decision) {
		t.Fatal("send_message alarm without expression_instruction was accepted")
	}
	decision.AlarmPlan.ExpressionInstruction = "Speak warmly and clearly."
	if !isValidCreateAlarmAction(decision) {
		t.Fatal("send_message alarm with expression_instruction was rejected")
	}
}

// TestScheduledSendMessagePreservesExpressionInstruction verifies the
// persisted alarm payload reaches the direct-commit chat plan unchanged.
func TestScheduledSendMessagePreservesExpressionInstruction(t *testing.T) {
	const (
		content               = "The prepared reminder."
		expressionInstruction = "Speak warmly and clearly."
	)
	result := Decide(context.Background(), &Situation{
		Source: SituationSourceExternal,
		Matter: SituationMatter{Event: &eventqueue.AgentEvent{
			Type:      eventqueue.EventTypeScheduled,
			SessionID: 7,
			Payload: &eventqueue.ScheduledEventPayload{
				ScheduledEventID:      11,
				Action:                model.ScheduledEventActionSendMessage,
				ActionContent:         content,
				ExpressionInstruction: expressionInstruction,
			},
		}},
	}, 3, nil)

	if len(result.Actions) != 1 || result.Actions[0].ChatPlan == nil {
		t.Fatalf("expected one chat action, got %#v", result.Actions)
	}
	plan := result.Actions[0].ChatPlan
	if plan.Content != content {
		t.Fatalf("content = %q, want %q", plan.Content, content)
	}
	if plan.ExpressionInstruction != expressionInstruction {
		t.Fatalf("expression instruction = %q, want %q", plan.ExpressionInstruction, expressionInstruction)
	}
}
