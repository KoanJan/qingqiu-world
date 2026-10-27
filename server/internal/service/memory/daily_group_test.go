package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/energy"
)

// TestDailyChatMessagesPreserveSpeaker verifies that a person's own observed
// speech remains their speech in the daily summary input.
func TestDailyChatMessagesPreserveSpeaker(t *testing.T) {
	db := recallTestDB(t)
	for _, person := range []model.Person{{ID: 12, Name: "当当"}, {ID: 1, Name: "Patrick"}} {
		if err := db.Create(&person).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.Session{ID: 24}).Error; err != nil {
		t.Fatal(err)
	}
	for _, personID := range []int64{12, 1} {
		if err := db.Create(&model.ParticipantSession{SessionID: 24, ParticipantID: personID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, energy.Timezone())
	for _, tc := range []struct {
		personID int64
		content  string
		want     string
	}{
		{12, "我写好了", `Chat message from session (session_id=24) — 当当 (self) [2026-10-20 12:00:00] said: "我写好了"`},
		{1, "我看到了", `Chat message from session (session_id=24) — Patrick [2026-10-20 12:00:00] said: "我看到了"`},
	} {
		message := model.Message{SessionID: 24, PersonID: tc.personID, Content: tc.content, CreatedAt: at}
		if err := db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID, CreatedAt: at}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.AgentObservation{PersonID: 12, EventID: event.ID, CreatedAt: at}).Error; err != nil {
			t.Fatal(err)
		}
		line, err := DescribeObservedEventAsExperience(context.Background(), 12, "当当", event.ID)
		if err != nil || line != tc.want || strings.Contains(line, "You said") {
			t.Fatalf("daily dialogue line = %q, err = %v; want %q", line, err, tc.want)
		}
	}
}

// TestDailyActionIncludesCommittedSpeech verifies that a Chat plan and its
// later sent message remain distinct in both the summary and source views.
func TestDailyActionIncludesCommittedSpeech(t *testing.T) {
	db := recallTestDB(t)
	at := time.Now().In(energy.Timezone()).AddDate(0, 0, -1).Truncate(time.Second)
	if err := db.Create(&model.Person{ID: 12, Name: "当当"}).Error; err != nil {
		t.Fatal(err)
	}
	decision := model.Decision{PersonID: 12, CreatedAt: at}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	action := model.Action{DecisionID: decision.ID, Type: model.ActionTypeChat, Status: model.ActionStatusEnded, Reason: "询问反馈", PlanJSON: `{"session_id":24,"guidance":"询问对方是否有空"}`, CreatedAt: at}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	before, err := dailyGroupsForDate(context.Background(), 12, at.Format("2006-01-02"))
	if err != nil || len(before) != 1 {
		t.Fatalf("daily action before effect: groups=%+v err=%v", before, err)
	}
	var unsent strings.Builder
	if err := formatDailyDecision(context.Background(), 12, &unsent, before[0], before[0].Decisions[0]); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unsent.String(), "What you intended") || strings.Contains(unsent.String(), "What followed:") {
		t.Fatalf("Chat without a recorded result was presented as a completed outcome: %s", unsent.String())
	}
	if strings.Contains(unsent.String(), "index was built") {
		t.Fatalf("daily input exposed index maintenance: %s", unsent.String())
	}
	unsentSources := []dailySource{{ID: action.ID, Kind: dailySourceAction}}
	if err := fillDailySourceText(context.Background(), 12, before[0].DayStart, before[0].DayEnd, unsentSources); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unsentSources[0].Text, "No sent message is recorded for this action on this day") || strings.Contains(unsentSources[0].Text, "source read") {
		t.Fatalf("daily source expansion obscures outcome or exposes implementation: %s", unsentSources[0].Text)
	}
	message := model.Message{PersonID: 12, SessionID: 24, Content: "你有时间看我发的故事吗？", CreatedAt: at.Add(time.Second)}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ActionEffect{ActionID: action.ID, EffectType: model.ActionEffectMessage, EffectID: message.ID, CreatedAt: message.CreatedAt}).Error; err != nil {
		t.Fatal(err)
	}
	groups, err := dailyGroupsForDate(context.Background(), 12, at.Format("2006-01-02"))
	if err != nil || len(groups) != 1 || len(groups[0].Effects[action.ID]) != 1 {
		t.Fatalf("daily action effects: groups=%+v err=%v", groups, err)
	}
	var input strings.Builder
	if err := formatDailyDecision(context.Background(), 12, &input, groups[0], groups[0].Decisions[0]); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"询问对方是否有空", "你有时间看我发的故事吗？", "What followed: At"} {
		if !strings.Contains(input.String(), expected) {
			t.Fatalf("daily input omitted %q: %s", expected, input.String())
		}
	}
	for _, forbidden := range []string{"session_id", "```json", "Recorded result", "plan_json"} {
		if strings.Contains(input.String(), forbidden) {
			t.Fatalf("daily input exposed %q: %s", forbidden, input.String())
		}
	}
	if strings.Contains(input.String(), "no message from you was sent") {
		t.Fatalf("daily input ignored the committed message: %s", input.String())
	}
	sources := []dailySource{{ID: action.ID, Kind: dailySourceAction}}
	if err := fillDailySourceText(context.Background(), 12, groups[0].DayStart, groups[0].DayEnd, sources); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sources[0].Text, fmt.Sprintf("%q", message.Content)) {
		t.Fatalf("source expansion omitted committed speech: %s", sources[0].Text)
	}
}

// TestDailyFeedbackJoinsProducingActionGroup verifies that one Action's result
// never becomes its own dated resource: it enriches that Action, and a later
// choice prompted by it stays in the same group.
func TestDailyFeedbackJoinsProducingActionGroup(t *testing.T) {
	db := recallTestDB(t)
	base := time.Date(2026, 10, 20, 13, 0, 0, 0, energy.Timezone())
	notice := model.Event{EventType: model.EventTypeSystemNotification, PayloadJSON: `"news"`, CreatedAt: base}
	if err := db.Create(&notice).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: 1, EventID: notice.ID, CreatedAt: base}).Error; err != nil {
		t.Fatal(err)
	}
	source := model.Decision{PersonID: 1, EventID: notice.ID, CreatedAt: base.Add(time.Minute)}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	action := model.Action{DecisionID: source.ID, Type: model.ActionTypeListReceivedJinshu, Status: model.ActionStatusEnded, CreatedAt: source.CreatedAt}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	feedback := model.Event{EventType: model.EventTypeJinshuListed, PayloadJSON: `{"Query":"weekend","Page":1,"Results":[{"JinshuID":41,"FromName":"Other","Topic":"Weekend plan"}]}`, CreatedAt: base.Add(2 * time.Minute)}
	if err := db.Create(&feedback).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: 1, EventID: feedback.ID, CreatedAt: feedback.CreatedAt}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ActionEffect{ActionID: action.ID, EffectType: model.ActionEffectSelfHeldEvent, EffectID: feedback.ID, CreatedAt: feedback.CreatedAt}).Error; err != nil {
		t.Fatal(err)
	}
	prompted := model.Decision{PersonID: 1, EventID: feedback.ID, CreatedAt: base.Add(3 * time.Minute)}
	if err := db.Create(&prompted).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Action{DecisionID: prompted.ID, Type: model.ActionTypeChat, Status: model.ActionStatusEnded, CreatedAt: prompted.CreatedAt}).Error; err != nil {
		t.Fatal(err)
	}

	groups, err := dailyGroupsForDate(context.Background(), 1, base.Format("2006-01-02"))
	if err != nil || len(groups) != 1 {
		t.Fatalf("feedback event became its own group: groups=%+v err=%v", groups, err)
	}
	group := groups[0]
	if group.Key != (dailyGroupKey{Type: model.DailyMemoryGroupSystemNotice, ID: 0}) || len(group.Observations) != 1 || len(group.Decisions) != 2 {
		t.Fatalf("feedback choice was not re-homed: %+v", group)
	}
	for _, source := range group.Sources {
		if source.Kind == dailySourceObservation && source.RefID == feedback.ID {
			t.Fatalf("feedback observation became an independent source: %+v", group.Sources)
		}
	}
	result, err := describeDailyActionEffect(context.Background(), 1, group.Effects[action.ID][0])
	if err != nil || !strings.Contains(result, "weekend") || !strings.Contains(result, "Weekend plan") || strings.Contains(result, "41") {
		t.Fatalf("feedback result = %q, err = %v", result, err)
	}
}

// TestDailyResourceGroupsUseInformationDensity verifies that high-information
// resources stay one-to-one while low-information biography collapses per day.
func TestDailyResourceGroupsUseInformationDensity(t *testing.T) {
	db := recallTestDB(t)
	base := time.Date(2026, 10, 20, 14, 0, 0, 0, energy.Timezone())
	add := func(eventType model.EventType, refID int64, at time.Time) {
		t.Helper()
		event := model.Event{EventType: eventType, RefID: refID, CreatedAt: at}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.AgentObservation{PersonID: 1, EventID: event.ID, CreatedAt: at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	add(model.EventTypeWorkCompleted, 501, base)
	add(model.EventTypeWorkCompleted, 502, base.Add(time.Minute))
	add(model.EventTypePSDigest, 601, base.Add(2*time.Minute))
	add(model.EventTypePSDigest, 602, base.Add(3*time.Minute))
	add(model.EventTypeBiography, 701, base.Add(4*time.Minute))
	add(model.EventTypeBiography, 702, base.Add(5*time.Minute))

	groups, err := dailyGroupsForDate(context.Background(), 1, base.Format("2006-01-02"))
	if err != nil || len(groups) != 5 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	byKey := make(map[dailyGroupKey]*dailyGroup, len(groups))
	for _, group := range groups {
		byKey[group.Key] = group
	}
	for _, key := range []dailyGroupKey{
		{Type: model.DailyMemoryGroupWork, ID: 501},
		{Type: model.DailyMemoryGroupWork, ID: 502},
		{Type: model.DailyMemoryGroupPSDigest, ID: 601},
		{Type: model.DailyMemoryGroupPSDigest, ID: 602},
	} {
		if byKey[key] == nil || len(byKey[key].Observations) != 1 {
			t.Fatalf("high-information group %+v was aggregated: %+v", key, byKey[key])
		}
	}
	if life := byKey[dailyGroupKey{Type: model.DailyMemoryGroupBiography, ID: 0}]; life == nil || len(life.Observations) != 2 {
		t.Fatalf("biography was not aggregated for the day: %+v", life)
	}
}

// TestDailyConversationDoesNotRepeatSentMessage keeps a committed message once
// in the conversation line and out of the action result lines.
func TestDailyConversationDoesNotRepeatSentMessage(t *testing.T) {
	db := recallTestDB(t)
	at := time.Date(2026, 10, 20, 15, 0, 0, 0, energy.Timezone())
	message := model.Message{SessionID: 24, PersonID: 12, Content: "我写好了", CreatedAt: at}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID, CreatedAt: at}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: 12, EventID: event.ID, CreatedAt: at}).Error; err != nil {
		t.Fatal(err)
	}
	decision := model.Decision{PersonID: 12, EventID: event.ID, CreatedAt: at}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	action := model.Action{DecisionID: decision.ID, Type: model.ActionTypeChat, Status: model.ActionStatusEnded, Reason: "答复", CreatedAt: at}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ActionEffect{ActionID: action.ID, EffectType: model.ActionEffectMessage, EffectID: message.ID, CreatedAt: at}).Error; err != nil {
		t.Fatal(err)
	}
	groups, err := dailyGroupsForDate(context.Background(), 12, at.Format("2006-01-02"))
	if err != nil || len(groups) != 1 || !groups[0].MessageIDs[message.ID] {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	var text strings.Builder
	if err := formatDailyDecision(context.Background(), 12, &text, groups[0], groups[0].Decisions[0]); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text.String(), "you sent") || strings.Contains(text.String(), "no message from you was sent") {
		t.Fatalf("a shown message was repeated as a result: %s", text.String())
	}
}

// TestDailyResultRecoversEarlierReason verifies that a result landing on one
// day carries only the reason of the earlier choice that set it in motion, and
// that another person's choice can never explain this person's result.
func TestDailyResultRecoversEarlierReason(t *testing.T) {
	db := recallTestDB(t)
	for _, person := range []model.Person{{ID: 1, Name: "Self"}, {ID: 2, Name: "Other"}} {
		if err := db.Create(&person).Error; err != nil {
			t.Fatal(err)
		}
	}
	today := time.Date(2026, 10, 20, 8, 0, 0, 0, energy.Timezone())
	yesterday := today.AddDate(0, 0, -1)
	startWork := func(personID int64, reason, background, description string) (model.Work, model.Event, model.AgentObservation) {
		t.Helper()
		decision := model.Decision{PersonID: personID, CreatedAt: yesterday}
		if err := db.Create(&decision).Error; err != nil {
			t.Fatal(err)
		}
		action := model.Action{DecisionID: decision.ID, Type: model.ActionTypeStartFocusedWork, Reason: reason, Background: background, CreatedAt: yesterday}
		if err := db.Create(&action).Error; err != nil {
			t.Fatal(err)
		}
		work := model.Work{PersonID: personID, Description: description}
		if err := db.Create(&work).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.ActionEffect{ActionID: action.ID, EffectType: model.ActionEffectWork, EffectID: work.ID, CreatedAt: today}).Error; err != nil {
			t.Fatal(err)
		}
		event := model.Event{EventType: model.EventTypeWorkCompleted, RefID: work.ID, CreatedAt: today}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		observation := model.AgentObservation{PersonID: 1, EventID: event.ID, CreatedAt: today}
		if err := db.Create(&observation).Error; err != nil {
			t.Fatal(err)
		}
		return work, event, observation
	}
	ownWork, ownEvent, _ := startWork(1, "想把故事写完", "读到一半", "写故事")
	otherWork, otherEvent, _ := startWork(2, "别人的理由", "别人的判断", "别人的工作")

	groups, err := dailyGroupsForDate(context.Background(), 1, today.Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	byKey := make(map[dailyGroupKey]*dailyGroup, len(groups))
	for _, group := range groups {
		byKey[group.Key] = group
	}
	own := byKey[dailyGroupKey{Type: model.DailyMemoryGroupWork, ID: ownWork.ID}]
	if own == nil || len(own.Decisions) != 0 {
		t.Fatalf("earlier choice was listed as today's decision: %+v", own)
	}
	motive, ok := own.Motives[ownEvent.ID]
	if !ok || motive.Reason != "想把故事写完" || motive.Background != "读到一半" {
		t.Fatalf("own earlier reason was not recovered: %+v", own.Motives)
	}
	other := byKey[dailyGroupKey{Type: model.DailyMemoryGroupWork, ID: otherWork.ID}]
	if other == nil {
		t.Fatal("another person's completed work was observed but not grouped")
	}
	if _, leaked := other.Motives[otherEvent.ID]; leaked {
		t.Fatalf("another person's choice explained this person's result: %+v", other.Motives)
	}
}

// TestDailyChatRecoversEarlierReason keeps a message that arrived today attached
// to the session while its motive comes from a choice made the day before.
func TestDailyChatRecoversEarlierReason(t *testing.T) {
	db := recallTestDB(t)
	if err := db.Create(&model.Person{ID: 1, Name: "Self"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Session{ID: 30}).Error; err != nil {
		t.Fatal(err)
	}
	today := time.Date(2026, 10, 20, 9, 0, 0, 0, energy.Timezone())
	decision := model.Decision{PersonID: 1, CreatedAt: today.AddDate(0, 0, -1)}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	action := model.Action{DecisionID: decision.ID, Type: model.ActionTypeChat, Reason: "想先问候一声", CreatedAt: decision.CreatedAt}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	message := model.Message{PersonID: 1, SessionID: 30, Content: "早", CreatedAt: today}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ActionEffect{ActionID: action.ID, EffectType: model.ActionEffectMessage, EffectID: message.ID, CreatedAt: today}).Error; err != nil {
		t.Fatal(err)
	}
	event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID, CreatedAt: today}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: 1, EventID: event.ID, CreatedAt: today}).Error; err != nil {
		t.Fatal(err)
	}
	groups, err := dailyGroupsForDate(context.Background(), 1, today.Format("2006-01-02"))
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	group := groups[0]
	if group.Key != (dailyGroupKey{Type: model.DailyMemoryGroupChat, ID: 30}) || len(group.Decisions) != 0 {
		t.Fatalf("earlier chat choice entered today's decisions: %+v", group)
	}
	if motive, ok := group.Motives[event.ID]; !ok || motive.Reason != "想先问候一声" {
		t.Fatalf("chat motive was not recovered: %+v", group.Motives)
	}
}

// TestDailyLaterOutcomeStaysOutOfCompletedDay verifies that a result recorded
// after midnight neither appears in the finished day nor makes its chat
// intention look silent.
func TestDailyLaterOutcomeStaysOutOfCompletedDay(t *testing.T) {
	db := recallTestDB(t)
	if err := db.Create(&model.Person{ID: 1, Name: "Self"}).Error; err != nil {
		t.Fatal(err)
	}
	decisionAt := time.Date(2026, 10, 20, 23, 50, 0, 0, energy.Timezone())
	decision := model.Decision{PersonID: 1, CreatedAt: decisionAt}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	action := model.Action{DecisionID: decision.ID, Type: model.ActionTypeChat, Reason: "回应", CreatedAt: decisionAt}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	messageAt := decisionAt.Add(20 * time.Minute)
	message := model.Message{PersonID: 1, SessionID: 30, Content: "还在吗", CreatedAt: messageAt}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ActionEffect{ActionID: action.ID, EffectType: model.ActionEffectMessage, EffectID: message.ID, CreatedAt: messageAt}).Error; err != nil {
		t.Fatal(err)
	}
	groups, err := dailyGroupsForDate(context.Background(), 1, decisionAt.Format("2006-01-02"))
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	var text strings.Builder
	if err := formatDailyDecision(context.Background(), 1, &text, groups[0], groups[0].Decisions[0]); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text.String(), "What followed") {
		t.Fatalf("a later outcome entered the completed day: %s", text.String())
	}
	if strings.Contains(text.String(), "no message from you was sent") {
		t.Fatalf("a committed message was misreported as silent: %s", text.String())
	}
	sources := append([]dailySource(nil), groups[0].Sources...)
	if err := fillDailySourceText(context.Background(), 1, groups[0].DayStart, groups[0].DayEnd, sources); err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || strings.Contains(sources[0].Text, message.Content) || !strings.Contains(sources[0].Text, "a later outcome remains possible") {
		t.Fatalf("old-day source included a later result or implied permanent silence: %+v", sources)
	}
}

// TestDailyDecisionFollowsItsOwnDay keeps a choice on the day it was made even
// when its triggering event reached the agent the day before.
func TestDailyDecisionFollowsItsOwnDay(t *testing.T) {
	db := recallTestDB(t)
	if err := db.Create(&model.Person{ID: 1, Name: "Self"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Session{ID: 30}).Error; err != nil {
		t.Fatal(err)
	}
	yesterday := time.Date(2026, 10, 19, 22, 0, 0, 0, energy.Timezone())
	today := time.Date(2026, 10, 20, 7, 0, 0, 0, energy.Timezone())
	message := model.Message{PersonID: 2, SessionID: 30, Content: "在吗", CreatedAt: yesterday}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID, CreatedAt: yesterday}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: 1, EventID: event.ID, CreatedAt: yesterday}).Error; err != nil {
		t.Fatal(err)
	}
	decision := model.Decision{PersonID: 1, EventID: event.ID, CreatedAt: today}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	action := model.Action{DecisionID: decision.ID, Type: model.ActionTypeChat, CreatedAt: today}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	past, err := dailyGroupsForDate(context.Background(), 1, yesterday.Format("2006-01-02"))
	if err != nil || len(past) != 1 || len(past[0].Decisions) != 0 {
		t.Fatalf("a later choice rewrote the earlier day: groups=%+v err=%v", past, err)
	}
	current, err := dailyGroupsForDate(context.Background(), 1, today.Format("2006-01-02"))
	if err != nil || len(current) != 1 || len(current[0].Observations) != 0 || len(current[0].Decisions) != 1 {
		t.Fatalf("the new choice was not dated to its own day: groups=%+v err=%v", current, err)
	}
}

// TestDailyExecutionSlotDecisionJoinsSourceGroup keeps the reopened slot notice
// out of the account while the choice it triggered stays with the Action that
// waited for the slot.
func TestDailyExecutionSlotDecisionJoinsSourceGroup(t *testing.T) {
	db := recallTestDB(t)
	if err := db.Create(&model.Person{ID: 1, Name: "Self"}).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 11, 0, 0, 0, energy.Timezone())
	waitDecision := model.Decision{PersonID: 1, CreatedAt: at.Add(-time.Hour)}
	if err := db.Create(&waitDecision).Error; err != nil {
		t.Fatal(err)
	}
	waitAction := model.Action{DecisionID: waitDecision.ID, Type: model.ActionTypeWaitForExecutionSlot, CreatedAt: at.Add(-time.Hour)}
	if err := db.Create(&waitAction).Error; err != nil {
		t.Fatal(err)
	}
	event := model.Event{EventType: model.EventTypeExecutionSlotAvailable, PayloadJSON: `"slot"`, CreatedAt: at}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ActionEffect{ActionID: waitAction.ID, EffectType: model.ActionEffectSelfHeldEvent, EffectID: event.ID, CreatedAt: at}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: 1, EventID: event.ID, CreatedAt: at}).Error; err != nil {
		t.Fatal(err)
	}
	decision := model.Decision{PersonID: 1, EventID: event.ID, CreatedAt: at}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Action{DecisionID: decision.ID, Type: model.ActionTypeStartFocusedWork, CreatedAt: at}).Error; err != nil {
		t.Fatal(err)
	}
	groups, err := dailyGroupsForDate(context.Background(), 1, at.Format("2006-01-02"))
	if err != nil || len(groups) != 1 {
		t.Fatalf("a reopened slot produced its own group: groups=%+v err=%v", groups, err)
	}
	group := groups[0]
	if group.Key != (dailyGroupKey{Type: model.DailyMemoryGroupHeartbeat}) {
		t.Fatalf("the follow-up choice left the waiting group: %+v", group.Key)
	}
	if len(group.Decisions) != 2 {
		t.Fatalf("the follow-up choice was dropped: %+v", group.Decisions)
	}
	for _, decision := range group.Decisions {
		if decision.Record.EventID == event.ID && len(decision.Actions) != 1 {
			t.Fatalf("the follow-up choice lost its action: %+v", decision)
		}
	}
}

// TestDailyExecutionSlotFollowUpKeepsItsOwnDay attaches a slot follow-up to its
// own day even when the waiting choice was made on an earlier, closed day.
func TestDailyExecutionSlotFollowUpKeepsItsOwnDay(t *testing.T) {
	db := recallTestDB(t)
	if err := db.Create(&model.Person{ID: 1, Name: "Self"}).Error; err != nil {
		t.Fatal(err)
	}
	yesterday := time.Date(2026, 10, 19, 18, 0, 0, 0, energy.Timezone())
	today := time.Date(2026, 10, 20, 9, 0, 0, 0, energy.Timezone())
	waitDecision := model.Decision{PersonID: 1, CreatedAt: yesterday}
	if err := db.Create(&waitDecision).Error; err != nil {
		t.Fatal(err)
	}
	waitAction := model.Action{DecisionID: waitDecision.ID, Type: model.ActionTypeWaitForExecutionSlot, CreatedAt: yesterday}
	if err := db.Create(&waitAction).Error; err != nil {
		t.Fatal(err)
	}
	event := model.Event{EventType: model.EventTypeExecutionSlotAvailable, PayloadJSON: `"slot"`, CreatedAt: today}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ActionEffect{ActionID: waitAction.ID, EffectType: model.ActionEffectSelfHeldEvent, EffectID: event.ID, CreatedAt: today}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: 1, EventID: event.ID, CreatedAt: today}).Error; err != nil {
		t.Fatal(err)
	}
	decision := model.Decision{PersonID: 1, EventID: event.ID, CreatedAt: today}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Action{DecisionID: decision.ID, Type: model.ActionTypeStartFocusedWork, CreatedAt: today}).Error; err != nil {
		t.Fatal(err)
	}
	past, err := dailyGroupsForDate(context.Background(), 1, yesterday.Format("2006-01-02"))
	if err != nil || len(past) != 1 || len(past[0].Decisions) != 1 {
		t.Fatalf("the earlier waiting choice was rewritten: groups=%+v err=%v", past, err)
	}
	current, err := dailyGroupsForDate(context.Background(), 1, today.Format("2006-01-02"))
	if err != nil || len(current) != 1 || len(current[0].Decisions) != 1 {
		t.Fatalf("the slot follow-up did not enter its own day: groups=%+v err=%v", current, err)
	}
	if current[0].Decisions[0].Record.ID != decision.ID {
		t.Fatalf("a closed day's choice was copied into the new day: %+v", current[0].Decisions)
	}
}

// TestDailyHeartbeatDecisionWithoutActionStillFormsAGroup verifies that the
// choice itself reaches the dated account with the reasoning it was made from,
// even when no Action followed it.
func TestDailyHeartbeatDecisionWithoutActionStillFormsAGroup(t *testing.T) {
	db := recallTestDB(t)
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, energy.Timezone())
	decision := model.Decision{PersonID: 12, CreatedAt: at, Thoughts: "Nothing here needs an answer right now."}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	groups, err := dailyGroupsForDate(context.Background(), 12, at.Format("2006-01-02"))
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	group := groups[0]
	if group.Key != (dailyGroupKey{Type: model.DailyMemoryGroupHeartbeat, ID: 0}) || len(group.Decisions) != 1 || len(group.Decisions[0].Actions) != 0 {
		t.Fatalf("heartbeat choice without an action was dropped: %+v", group)
	}
	if !group.StartAt.Equal(at) || !group.EndAt.Equal(at) {
		t.Fatalf("heartbeat contact range=%v..%v, want %v", group.StartAt, group.EndAt, at)
	}
	var text strings.Builder
	if err := formatDailyDecision(context.Background(), 12, &text, group, group.Decisions[0]); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), decision.Thoughts) {
		t.Fatalf("decision reasoning was lost: %s", text.String())
	}
	if len(group.Sources) != 1 || group.Sources[0].Kind != dailySourceDecision || group.Sources[0].ID != decision.ID {
		t.Fatalf("actionless decision has no source: %+v", group.Sources)
	}
	shard := model.DailyMemory{PersonID: 12, LocalDate: at.Format("2006-01-02"), GroupType: group.Key.Type, GroupID: group.Key.ID, Summary: "A choice without action", StartAt: group.StartAt, EndAt: group.EndAt, Embedding: []byte{}}
	if err := db.Create(&shard).Error; err != nil {
		t.Fatal(err)
	}
	items, more, _, err := ReadDailyMemorySources(context.Background(), 12, shard.ID, 5, 0)
	if err != nil || more || len(items) != 1 || items[0].DecisionID != decision.ID || !strings.Contains(items[0].Text, decision.Thoughts) {
		t.Fatalf("actionless decision source=%+v more=%v err=%v", items, more, err)
	}
	if _, _, _, err := ReadDailyMemorySources(context.Background(), 13, shard.ID, 5, 0); err == nil {
		t.Fatal("another person read the decision source")
	}
}

// TestDailyJinshuEffectRequiresSenderOwnership keeps another person's jinshu
// from being exposed through an Action effect attributed to the observer.
func TestDailyJinshuEffectRequiresSenderOwnership(t *testing.T) {
	db := recallTestDB(t)
	for _, person := range []model.Person{{ID: 1, Name: "Self"}, {ID: 2, Name: "Other"}, {ID: 3, Name: "Recipient"}} {
		if err := db.Create(&person).Error; err != nil {
			t.Fatal(err)
		}
	}
	jinshu := model.Jinshu{FromPersonID: 2, ToPersonID: 3, Topic: "Private", Description: "Other person's letter"}
	if err := db.Create(&jinshu).Error; err != nil {
		t.Fatal(err)
	}
	effect := model.ActionEffect{ActionID: 1, EffectType: model.ActionEffectJinshu, EffectID: jinshu.ID, CreatedAt: time.Now()}
	if result, err := describeDailyActionEffect(context.Background(), 1, effect); err == nil || result != "" {
		t.Fatalf("foreign jinshu was disclosed: result=%q err=%v", result, err)
	}
	result, err := describeDailyActionEffect(context.Background(), 2, effect)
	if err != nil || !strings.Contains(result, jinshu.Description) {
		t.Fatalf("sender could not read their jinshu result: result=%q err=%v", result, err)
	}
}

// TestDailyDropsLegacyDecisionWithoutContent verifies that a decision stored
// before reasoning was persisted, and with no Action, contributes nothing: it
// neither forms a day of its own nor stretches a day that has other content.
func TestDailyDropsLegacyDecisionWithoutContent(t *testing.T) {
	db := recallTestDB(t)
	date := time.Date(2026, 10, 20, 0, 0, 0, 0, energy.Timezone())
	legacy := model.Decision{PersonID: 12, CreatedAt: date.Add(8 * time.Hour)}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	groups, err := dailyGroupsForDate(context.Background(), 12, "2026-10-20")
	if err != nil || len(groups) != 0 {
		t.Fatalf("legacy empty decision formed a group: %+v err=%v", groups, err)
	}
	readable := model.Decision{PersonID: 12, CreatedAt: date.Add(12 * time.Hour), Thoughts: "I am waiting for a reply."}
	if err := db.Create(&readable).Error; err != nil {
		t.Fatal(err)
	}
	groups, err = dailyGroupsForDate(context.Background(), 12, "2026-10-20")
	if err != nil || len(groups) != 1 || len(groups[0].Decisions) != 1 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	if groups[0].Decisions[0].Record.ID != readable.ID {
		t.Fatalf("legacy decision was kept: %+v", groups[0].Decisions)
	}
	if !groups[0].StartAt.Equal(readable.CreatedAt) || !groups[0].EndAt.Equal(readable.CreatedAt) {
		t.Fatalf("contact range included the dropped decision: %v..%v", groups[0].StartAt, groups[0].EndAt)
	}
}

// TestDailyChatNamesTheOtherParticipant verifies that a message the observer
// wrote still names whom it was addressed to, so the counterpart cannot become
// an unnamed "other person" in the account.
func TestDailyChatNamesTheOtherParticipant(t *testing.T) {
	db := recallTestDB(t)
	for _, person := range []model.Person{{ID: 12, Name: "当当"}, {ID: 1, Name: "Patrick"}} {
		if err := db.Create(&person).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.Session{ID: 24}).Error; err != nil {
		t.Fatal(err)
	}
	for _, personID := range []int64{12, 1} {
		if err := db.Create(&model.ParticipantSession{SessionID: 24, ParticipantID: personID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, energy.Timezone())
	message := model.Message{SessionID: 24, PersonID: 12, Content: "你有时间看我发的故事吗？", CreatedAt: at}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID, CreatedAt: at}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: 12, EventID: event.ID, CreatedAt: at}).Error; err != nil {
		t.Fatal(err)
	}
	line, err := dailyObservedEvent(context.Background(), 12, "当当", at, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "said to Patrick") || !strings.Contains(line, message.Content) {
		t.Fatalf("daily line lost the addressee: %s", line)
	}
}

// TestDailySummaryAvoidsIndexInternals keeps storage and refresh terminology
// out of both the retrospective request and the source account shown later.
func TestDailySummaryAvoidsIndexInternals(t *testing.T) {
	for _, forbidden := range []string{"semantic index", "index time", "indexed date", "record IDs", "source record", "JSON plan", "index was built", "source read", "source group", "without a new event", "you chose the following actions", "recorded result"} {
		if strings.Contains(strings.ToLower(dailySummaryPrompt), strings.ToLower(forbidden)) {
			t.Fatalf("daily summary prompt exposes %q", forbidden)
		}
	}
	// The retrospective must ground the agent's digital existence; without it
	// the account invented bodily experiences such as waking from sleep.
	for _, required := range []string{"persistent digital consciousness", "no physical body", "human senses", "falling asleep"} {
		if !strings.Contains(strings.ToLower(dailySummaryPrompt), strings.ToLower(required)) {
			t.Fatalf("daily summary prompt omitted world grounding %q", required)
		}
	}
}
