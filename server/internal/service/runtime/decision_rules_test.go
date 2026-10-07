package runtime

import (
	"context"
	"testing"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/eventqueue"
)

// TestRuleDecideKeepsEventBranchesIndependentOfTheModel checks the exact
// no-model decisions for each rule-based event that reaches Decide.
func TestRuleDecideKeepsEventBranchesIndependentOfTheModel(t *testing.T) {
	for _, spec := range []struct {
		name     string
		event    eventqueue.AgentEvent
		accepted bool
		chats    int
		content  string
	}{
		{"group joined", eventqueue.AgentEvent{Type: eventqueue.EventTypeGroupChatJoined}, true, 0, ""},
		{"group left", eventqueue.AgentEvent{Type: eventqueue.EventTypeGroupChatLeft}, true, 0, ""},
		{"system notice", eventqueue.AgentEvent{Type: eventqueue.EventTypeSystemNotification}, true, 0, ""},
		{"alarm created", eventqueue.AgentEvent{Type: eventqueue.EventTypeAlarmCreated}, true, 0, ""},
		{"private space completed", eventqueue.AgentEvent{Type: eventqueue.EventTypePSCompleted}, true, 0, ""},
		{"cancelled work completed", eventqueue.AgentEvent{Type: eventqueue.EventTypeWorkCompleted, Payload: &eventqueue.WorkCompletedPayload{
			WorkID: 19, Status: "abandoned", CancelActionID: 815, CancelReason: "Patrick asked me to stop",
		}}, true, 0, ""},
		{"session alarm", eventqueue.AgentEvent{Type: eventqueue.EventTypeScheduled, SessionID: 7}, true, 1, ""},
		{"prepared session alarm", eventqueue.AgentEvent{Type: eventqueue.EventTypeScheduled, SessionID: 7, Payload: &eventqueue.ScheduledEventPayload{
			ScheduledEventID: 11, Action: model.ScheduledEventActionSendMessage,
			ActionContent: "Prepared answer", ExpressionInstruction: "Speak clearly",
		}}, true, 1, "Prepared answer"},
		{"invalid prepared alarm", eventqueue.AgentEvent{Type: eventqueue.EventTypeScheduled, SessionID: 7, Payload: &eventqueue.ScheduledEventPayload{
			ScheduledEventID: 12, Action: model.ScheduledEventActionSendMessage,
			ActionContent: "Prepared answer",
		}}, false, 0, ""},
	} {
		t.Run(spec.name, func(t *testing.T) {
			result := Decide(context.Background(), &Situation{
				Source: SituationSourceExternal,
				Matter: SituationMatter{Event: &spec.event},
			}, 999, nil)
			if result.Accepted != spec.accepted || len(result.Actions) != spec.chats {
				t.Fatalf("rule changed its result: %+v", result)
			}
			for _, act := range result.Actions {
				if act.Type != action.Chat || act.ChatPlan == nil || act.ChatPlan.SessionID != spec.event.SessionID || act.ChatPlan.Content != spec.content {
					t.Fatalf("session alarm did not keep its target and content: %+v", act)
				}
			}
		})
	}
}
