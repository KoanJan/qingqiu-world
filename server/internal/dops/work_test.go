package dops

import (
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// TestListSessionActivityWorksFollowsRecordedCauses verifies that Activity
// includes a same-person Work behind a result Event, but does not treat a
// heartbeat or another person's reaction as a continuation of the Session.
func TestListSessionActivityWorksFollowsRecordedCauses(t *testing.T) {
	t.Setenv("DATA_ROOT", t.TempDir())
	t.Setenv("LOG_DIR", filepath.Join(t.TempDir(), "logs"))
	previousDB := database.DB
	database.Init()
	t.Cleanup(func() { database.DB = previousDB })

	create := func(row any) {
		t.Helper()
		if err := database.DB.Create(row).Error; err != nil {
			t.Fatalf("create %T: %v", row, err)
		}
	}
	message := &model.Message{SessionID: 23, PersonID: 1, Content: "Build the game"}
	create(message)
	messageEvent := &model.Event{EventType: model.EventTypeMessage, RefID: message.ID}
	create(messageEvent)
	firstDecision := &model.Decision{PersonID: 12, EventID: messageEvent.ID}
	create(firstDecision)
	firstAction := &model.Action{DecisionID: firstDecision.ID, Type: model.ActionTypeStartFocusedWork}
	create(firstAction)
	firstWork := &model.Work{PersonID: 12, SessionID: 23, Description: "Build the game"}
	create(firstWork)
	create(&model.ActionEffect{ActionID: firstAction.ID, EffectType: model.ActionEffectWork, EffectID: firstWork.ID})

	completedEvent := &model.Event{EventType: model.EventTypeWorkCompleted, RefID: firstWork.ID}
	create(completedEvent)
	inspectDecision := &model.Decision{PersonID: 12, EventID: completedEvent.ID}
	create(inspectDecision)
	inspectAction := &model.Action{DecisionID: inspectDecision.ID, Type: model.ActionTypeInspectOwnedSpace}
	create(inspectAction)
	inspectedEvent := &model.Event{EventType: model.EventTypeOwnedSpaceInspected, PayloadJSON: `{"result":"ready"}`}
	create(inspectedEvent)
	create(&model.ActionEffect{ActionID: inspectAction.ID, EffectType: model.ActionEffectSelfHeldEvent, EffectID: inspectedEvent.ID})
	continuationDecision := &model.Decision{PersonID: 12, EventID: inspectedEvent.ID}
	create(continuationDecision)
	continuationAction := &model.Action{DecisionID: continuationDecision.ID, Type: model.ActionTypeStartFocusedWork}
	create(continuationAction)
	continuationWork := &model.Work{PersonID: 12, SessionID: 0, Description: "Continue the game"}
	create(continuationWork)
	create(&model.ActionEffect{ActionID: continuationAction.ID, EffectType: model.ActionEffectWork, EffectID: continuationWork.ID})

	heartbeatDecision := &model.Decision{PersonID: 12, EventID: 0}
	create(heartbeatDecision)
	heartbeatAction := &model.Action{DecisionID: heartbeatDecision.ID, Type: model.ActionTypeStartFocusedWork}
	create(heartbeatAction)
	heartbeatWork := &model.Work{PersonID: 12, SessionID: 0, Description: "Unlinked heartbeat work"}
	create(heartbeatWork)
	create(&model.ActionEffect{ActionID: heartbeatAction.ID, EffectType: model.ActionEffectWork, EffectID: heartbeatWork.ID})

	otherDecision := &model.Decision{PersonID: 13, EventID: completedEvent.ID}
	create(otherDecision)
	otherAction := &model.Action{DecisionID: otherDecision.ID, Type: model.ActionTypeStartFocusedWork}
	create(otherAction)
	otherWork := &model.Work{PersonID: 13, SessionID: 0, Description: "Another person's work"}
	create(otherWork)
	create(&model.ActionEffect{ActionID: otherAction.ID, EffectType: model.ActionEffectWork, EffectID: otherWork.ID})

	legacyWork := &model.Work{PersonID: 12, SessionID: 23, Description: "Older Work without source links"}
	create(legacyWork)
	// A Work may have an explicit Session origin even when its Decision was
	// triggered by a non-message Event. Its descendants retain that origin.
	scheduledEvent := &model.Event{EventType: model.EventTypeScheduled, RefID: 77}
	create(scheduledEvent)
	scheduledDecision := &model.Decision{PersonID: 12, EventID: scheduledEvent.ID}
	create(scheduledDecision)
	scheduledAction := &model.Action{DecisionID: scheduledDecision.ID, Type: model.ActionTypeStartFocusedWork}
	create(scheduledAction)
	scheduledWork := &model.Work{PersonID: 12, SessionID: 23, Description: "Scheduled session work"}
	create(scheduledWork)
	create(&model.ActionEffect{ActionID: scheduledAction.ID, EffectType: model.ActionEffectWork, EffectID: scheduledWork.ID})
	scheduledCompleted := &model.Event{EventType: model.EventTypeWorkCompleted, RefID: scheduledWork.ID}
	create(scheduledCompleted)
	scheduledContinuationDecision := &model.Decision{PersonID: 12, EventID: scheduledCompleted.ID}
	create(scheduledContinuationDecision)
	scheduledContinuationAction := &model.Action{DecisionID: scheduledContinuationDecision.ID, Type: model.ActionTypeStartFocusedWork}
	create(scheduledContinuationAction)
	scheduledContinuationWork := &model.Work{PersonID: 12, SessionID: 0, Description: "Continue scheduled work"}
	create(scheduledContinuationWork)
	create(&model.ActionEffect{ActionID: scheduledContinuationAction.ID, EffectType: model.ActionEffectWork, EffectID: scheduledContinuationWork.ID})

	works, err := ListSessionActivityWorks(23)
	if err != nil {
		t.Fatalf("list session works: %v", err)
	}
	got := make(map[int64]bool, len(works))
	for _, work := range works {
		got[work.ID] = true
	}
	if len(got) != 5 || !got[firstWork.ID] || !got[continuationWork.ID] || !got[legacyWork.ID] || !got[scheduledWork.ID] || !got[scheduledContinuationWork.ID] || got[heartbeatWork.ID] || got[otherWork.ID] {
		t.Fatalf("session work IDs = %v; want only explicit origins and their causal descendants", got)
	}

	for _, workID := range []int64{firstWork.ID, continuationWork.ID, heartbeatWork.ID, otherWork.ID} {
		create(&model.Interaction{WorkID: workID, Iteration: 1, Type: model.InteractionTypeResponse, Data: `{"content":"done"}`})
	}
	workIDs := make([]int64, 0, len(works))
	for _, work := range works {
		workIDs = append(workIDs, work.ID)
	}
	interactions, hasMore, err := ListActivityInteractions(workIDs, 0, 0, 10)
	if err != nil || hasMore || len(interactions) != 2 || interactions[0].WorkID != firstWork.ID || interactions[1].WorkID != continuationWork.ID {
		t.Fatalf("causal activity interactions = %+v, hasMore=%t, err=%v", interactions, hasMore, err)
	}
}
