package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// recallTestDB installs an isolated database with the production recall indexes
// and restores package globals when the test or benchmark ends.
func recallTestDB(t testing.TB) *gorm.DB {
	t.Helper()
	old := database.DB
	previousIndexReady := termIndexReady.Load()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/recall.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.Session{}, &model.ParticipantSession{}, &model.Message{}, &model.Event{}, &model.AgentObservation{}, &model.Decision{}, &model.Action{}, &model.ActionEffect{}, &model.Work{}, &model.FocusHandoff{}, &model.MemoryTerm{}, &model.EntityProfile{}, &model.Jinshu{}, &model.AgentBiography{}, &model.PSDigest{}, &model.ScheduledEvent{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() {
		database.DB = old
		termIndexReady.Store(previousIndexReady)
	})
	if err := database.EnsureRecallIndexes(); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestObservedMessagesNameTheirSpeakers keeps the agent-facing record distinct
// from the person-ID source text used by the lexical index.
func TestObservedMessagesNameTheirSpeakers(t *testing.T) {
	db := recallTestDB(t)
	for _, person := range []model.Person{{ID: 13, Name: "粒粒"}, {ID: 1, Name: "Patrick"}} {
		if err := db.Create(&person).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.Session{ID: 25}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ParticipantSession{SessionID: 25, ParticipantID: 13}).Error; err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct {
		personID int64
		content  string
		wanted   string
	}{
		{1, "你好呀粒粒", "Chat message from session (session_id=25) — Patrick said: \"你好呀粒粒\"."},
		{13, "嗯", "Chat message from session (session_id=25) — You said: \"嗯\"."},
	} {
		message := model.Message{SessionID: 25, PersonID: spec.personID, Content: spec.content}
		if err := db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.AgentObservation{PersonID: 13, EventID: event.ID}).Error; err != nil {
			t.Fatal(err)
		}
		observed, err := DescribeObservedEvent(13, event.ID)
		if err != nil || !strings.Contains(observed, spec.wanted) {
			t.Fatalf("recent experience speaker: %q, err=%v", observed, err)
		}
		item, ok, err := readRecallItem(13, model.MemorySourceEvent, event.ID)
		if err != nil || !ok || !strings.Contains(item.Text, spec.wanted) {
			t.Fatalf("recall speaker: %+v, ok=%v, err=%v", item, ok, err)
		}
		indexed, _, err := eventSourceContent(event)
		if err != nil || !strings.Contains(indexed, fmt.Sprintf("Person %d said", spec.personID)) {
			t.Fatalf("neutral index source changed: %q, err=%v", indexed, err)
		}
	}
}

// BenchmarkRecallMessageHistory measures bounded recent and lexical reads
// against a longer observed conversation with the production indexes.
func BenchmarkRecallMessageHistory(b *testing.B) {
	db := recallTestDB(b)
	if err := db.Create(&model.ParticipantSession{SessionID: 10, ParticipantID: 1}).Error; err != nil {
		b.Fatal(err)
	}
	const count = 3000
	base := time.Now().Add(-time.Duration(count+60) * time.Second).Truncate(time.Second)
	messages := make([]model.Message, count)
	events := make([]model.Event, count)
	observations := make([]model.AgentObservation, count)
	terms := make([]model.MemoryTerm, 0, count*3)
	for i := 0; i < count; i++ {
		id := int64(i + 1)
		at := base.Add(time.Duration(i) * time.Second)
		messages[i] = model.Message{ID: id, SessionID: 10, PersonID: 2, Content: "青丘历史消息", CreatedAt: at}
		events[i] = model.Event{ID: id, EventType: model.EventTypeMessage, RefID: id, CreatedAt: at}
		observations[i] = model.AgentObservation{ID: id, PersonID: 1, EventID: id}
		for _, term := range searchTerms("青丘") {
			terms = append(terms, model.MemoryTerm{Term: term, SourceKind: model.MemorySourceEvent, SourceID: id, SourceCreatedAt: at, SourceVersion: 1})
		}
	}
	for _, rows := range []any{&messages, &events, &observations, &terms} {
		if err := db.CreateInBatches(rows, 200).Error; err != nil {
			b.Fatal(err)
		}
	}
	if err := db.Exec("ANALYZE").Error; err != nil {
		b.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		scope RecallScope
		query string
	}{
		{"recent_message", RecallMessages, ""},
		{"lexical_message", RecallMessages, "青丘"},
		{"lexical_cross_source", RecallAll, "青丘"},
	} {
		b.Run(test.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				page, err := Recall(RecallRequest{Scope: test.scope, PersonID: 1, Query: test.query})
				if err != nil || len(page.Items) != 5 {
					b.Fatalf("recall failed: %+v err=%v", page, err)
				}
			}
		})
	}
}

// TestRecallHonorsObservationAndHistoricalMembership checks that an index hit
// requires Observation and participation even after a Session is soft-deleted.
func TestRecallHonorsObservationAndHistoricalMembership(t *testing.T) {
	db := recallTestDB(t)
	for _, sessionID := range []int64{10, 20} {
		if err := db.Create(&model.Session{ID: sessionID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.ParticipantSession{SessionID: 10, ParticipantID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	var eventIDs []int64
	var messageIDs []int64
	for i, spec := range []struct {
		session int64
		content string
		observe bool
	}{
		{10, "青丘委托一", true}, {10, "青丘未读二", false}, {20, "青丘越权三", true},
	} {
		message := model.Message{SessionID: spec.session, PersonID: 2, Content: spec.content, CreatedAt: base.Add(time.Duration(i) * time.Minute)}
		if err := db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID, CreatedAt: message.CreatedAt}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		if spec.observe {
			if err := CreateObservation(1, event.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err := RefreshSource(model.MemorySourceEvent, event.ID); err != nil {
			t.Fatal(err)
		}
		eventIDs = append(eventIDs, event.ID)
		messageIDs = append(messageIDs, message.ID)
	}
	if err := CreateObservation(1, eventIDs[0]); err != nil {
		t.Fatalf("duplicate observation was not idempotent: %v", err)
	}
	page, err := Recall(RecallRequest{Scope: RecallMessages, PersonID: 1, Query: "青丘"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].SourceID != eventIDs[0] || !strings.Contains(page.Items[0].Text, "委托一") {
		t.Fatalf("private or unread message leaked, or observed message was lost: %+v", page)
	}
	if err := db.Model(&model.Session{}).Where("id = ?", 10).Update("status", model.SessionStatusDeleted).Error; err != nil {
		t.Fatal(err)
	}
	closed, err := Recall(RecallRequest{Scope: RecallMessages, PersonID: 1, Query: "青丘"})
	if err != nil || len(closed.Items) != 1 || closed.Items[0].SourceID != eventIDs[0] {
		t.Fatalf("soft deletion lost observed history or exposed unread history: %+v err=%v", closed, err)
	}
	rows, err := SearchObservedMessages(1, 10, 0, []string{"青丘"}, 5)
	if err != nil || len(rows) != 1 || rows[0].Content != "青丘委托一" {
		t.Fatalf("chat history scope failed: rows=%+v err=%v", rows, err)
	}
	within, err := Recall(RecallRequest{Scope: RecallMessages, PersonID: 1, FromTime: base.UTC(), ToTime: base.Add(time.Minute).UTC()})
	if err != nil || len(within.Items) != 1 || within.Items[0].SourceID != eventIDs[0] {
		t.Fatalf("half-open UTC time range failed: %+v err=%v", within, err)
	}
	decision := model.Decision{PersonID: 1, EventID: eventIDs[0]}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	action := model.Action{DecisionID: decision.ID, Type: model.ActionTypeChat, Status: model.ActionStatusEnded}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ActionEffect{ActionID: action.ID, EffectType: model.ActionEffectMessage, EffectID: messageIDs[0]}).Error; err != nil {
		t.Fatal(err)
	}
	linked, err := Recall(RecallRequest{Scope: RecallEvents, PersonID: 1, ActionID: action.ID})
	if err != nil || len(linked.Items) != 1 || linked.Items[0].SourceID != eventIDs[0] {
		t.Fatalf("action effect did not lead to observed result event: %+v err=%v", linked, err)
	}
	if err := db.Model(&model.Message{}).Where("id = ?", messageIDs[0]).Update("content", "新委托一").Error; err != nil {
		t.Fatal(err)
	}
	if err := RebuildTermIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	stale, err := Recall(RecallRequest{Scope: RecallMessages, PersonID: 1, Query: "青丘"})
	if err != nil || len(stale.Items) != 0 {
		t.Fatalf("event index retained changed source text: %+v err=%v", stale, err)
	}
	updated, err := Recall(RecallRequest{Scope: RecallMessages, PersonID: 1, Query: "新委托"})
	if err != nil || len(updated.Items) != 1 || updated.Items[0].SourceID != eventIDs[0] {
		t.Fatalf("event index did not refresh changed source text: %+v err=%v", updated, err)
	}
	if err := db.Where("session_id = ? AND participant_id = ?", 10, 1).Delete(&model.ParticipantSession{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, req := range []RecallRequest{
		{Scope: RecallMessages, PersonID: 1, Query: "新委托"},
		{Scope: RecallMessages, PersonID: 1, SourceID: eventIDs[0]},
		{Scope: RecallEvents, PersonID: 1, ActionID: action.ID},
	} {
		page, err := Recall(req)
		if err != nil || len(page.Items) != 0 {
			t.Fatalf("former member retained message access: request=%+v page=%+v err=%v", req, page, err)
		}
	}
}

// TestRecallPagesAndDecisionRelations verifies stable continuation and that
// same-decision actions are available only to the action owner.
func TestRecallPagesAndDecisionRelations(t *testing.T) {
	db := recallTestDB(t)
	decision := model.Decision{PersonID: 1}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		row := model.Action{DecisionID: decision.ID, Type: model.ActionTypeChat, Status: model.ActionStatusEnded, Background: "答应联系", Reason: "履行委托", PlanJSON: `{"session_id":10}`, CreatedAt: time.Now().Add(time.Duration(i-10) * time.Minute)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if err := RefreshSource(model.MemorySourceAction, row.ID); err != nil {
			t.Fatal(err)
		}
	}
	req := RecallRequest{Scope: RecallActions, PersonID: 1, DecisionID: decision.ID, PageSize: 2}
	first, err := Recall(req)
	if err != nil || !first.HasMore || len(first.Items) != 2 {
		t.Fatalf("first page: %+v err=%v", first, err)
	}
	req.Cursor = first.NextCursor
	second, err := Recall(req)
	if err != nil || second.HasMore || len(second.Items) != 1 {
		t.Fatalf("second page: %+v err=%v", second, err)
	}
	if first.Items[0].SourceID == second.Items[0].SourceID || first.Items[1].SourceID == second.Items[0].SourceID {
		t.Fatal("cursor repeated an action")
	}
	changed := req
	changed.Query = "委托"
	if _, err := Recall(changed); err == nil {
		t.Fatal("cursor accepted changed query")
	}
	req.PersonID = 2
	req.Cursor = ""
	foreign, err := Recall(req)
	if err != nil || len(foreign.Items) != 0 {
		t.Fatalf("foreign private actions exposed: %+v err=%v", foreign, err)
	}
}

// TestWorkAdjustmentAndIndexRefresh covers the Work-to-Action reverse path
// and replacement of a mutable Work's old lexical keys.
func TestWorkAdjustmentAndIndexRefresh(t *testing.T) {
	db := recallTestDB(t)
	work := model.Work{PersonID: 1, Description: "青丘旧方向", Status: model.WorkStatusRunning}
	if err := db.Create(&work).Error; err != nil {
		t.Fatal(err)
	}
	decision := model.Decision{PersonID: 1}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	action := model.Action{DecisionID: decision.ID, Type: model.ActionTypeRouteFocusedWork, PlanJSON: `{"target_work_id":1,"guidance":"改成新的方向"}`, Status: model.ActionStatusEnded}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&action).Update("plan_json", `{"target_work_id":`+fmt.Sprint(work.ID)+`,"guidance":"改成新的方向"}`).Error; err != nil {
		t.Fatal(err)
	}
	origin := model.Action{DecisionID: decision.ID, Type: model.ActionTypeStartFocusedWork, Status: model.ActionStatusEnded, Reason: "开始任务"}
	if err := db.Create(&origin).Error; err != nil {
		t.Fatal(err)
	}
	cancel := model.Action{DecisionID: decision.ID, Type: model.ActionTypeCancelFocusedWork, PlanJSON: `{"target_work_id":` + fmt.Sprint(work.ID) + `,"guidance":"停止工作"}`, Status: model.ActionStatusEnded}
	if err := db.Create(&cancel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ActionEffect{ActionID: origin.ID, EffectType: model.ActionEffectWork, EffectID: work.ID}).Error; err != nil {
		t.Fatal(err)
	}
	for _, accepted := range []int64{action.ID, cancel.ID} {
		if err := db.Create(&model.ActionEffect{ActionID: accepted, EffectType: model.ActionEffectWorkControl, EffectID: work.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := RebuildTermIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	linked, err := Recall(RecallRequest{Scope: RecallActions, PersonID: 1, WorkID: work.ID})
	if err != nil || len(linked.Items) != 3 {
		t.Fatalf("work creation, route and cancellation not all found: %+v err=%v", linked, err)
	}
	found := make(map[int64]bool, len(linked.Items))
	for _, item := range linked.Items {
		found[item.SourceID] = true
	}
	if !found[origin.ID] || !found[action.ID] || !found[cancel.ID] {
		t.Fatalf("work relation omitted an action: %+v", linked)
	}
	if err := db.Model(&work).Update("description", "青丘新方向").Error; err != nil {
		t.Fatal(err)
	}
	if err := RefreshSource(model.MemorySourceWork, work.ID); err != nil {
		t.Fatal(err)
	}
	old, err := Recall(RecallRequest{Scope: RecallWorks, PersonID: 1, Query: "旧方向"})
	if err != nil || len(old.Items) != 0 {
		t.Fatalf("stale Work term survived: %+v err=%v", old, err)
	}
	updated, err := Recall(RecallRequest{Scope: RecallWorks, PersonID: 1, Query: "新方向"})
	if err != nil || len(updated.Items) != 1 {
		t.Fatalf("updated Work term absent: %+v err=%v", updated, err)
	}
}

// TestRecallEventBranchesChecks that each event source keeps its own access
// rule and that continuation still orders messages by Message.CreatedAt.
func TestRecallEventBranches(t *testing.T) {
	db := recallTestDB(t)
	if err := db.Create(&model.ParticipantSession{SessionID: 10, ParticipantID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	message := model.Message{SessionID: 10, PersonID: 2, Content: "第一条消息", CreatedAt: base.Add(3 * time.Minute)}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	work := model.Work{PersonID: 1, Description: "继续整理", Status: model.WorkStatusCompleted}
	if err := db.Create(&work).Error; err != nil {
		t.Fatal(err)
	}
	letter := model.Jinshu{FromPersonID: 2, ToPersonID: 1, Topic: "交付说明"}
	if err := db.Create(&letter).Error; err != nil {
		t.Fatal(err)
	}
	otherLetter := model.Jinshu{FromPersonID: 2, ToPersonID: 3, Topic: "私人内容"}
	if err := db.Create(&otherLetter).Error; err != nil {
		t.Fatal(err)
	}
	specs := []struct {
		typeID model.EventType
		refID  int64
		body   string
		at     time.Time
	}{
		{model.EventTypeMessage, message.ID, "", base},
		{model.EventTypeWorkCompleted, work.ID, "", base.Add(time.Minute)},
		{model.EventTypeJinshu, letter.ID, "", base.Add(2 * time.Minute)},
		{model.EventTypeJinshu, otherLetter.ID, "", base.Add(4 * time.Minute)},
		{model.EventTypeOwnedSpaceInspected, 0, `{"result":"checked"}`, base.Add(5 * time.Minute)},
	}
	var ids []int64
	for _, spec := range specs {
		event := model.Event{EventType: spec.typeID, RefID: spec.refID, PayloadJSON: spec.body, CreatedAt: spec.at}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		if err := CreateObservation(1, event.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, event.ID)
	}
	req := RecallRequest{Scope: RecallEvents, PersonID: 1, PageSize: 2}
	want := []int64{ids[4], ids[0], ids[2], ids[1]}
	var got []int64
	for {
		page, err := Recall(req)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			got = append(got, item.SourceID)
		}
		if !page.HasMore {
			break
		}
		req.Cursor = page.NextCursor
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("event branch permission or chronology changed: got %v, want %v", got, want)
	}
	window, err := Recall(RecallRequest{Scope: RecallEvents, PersonID: 1, FromTime: base.Add(2 * time.Minute), ToTime: base.Add(3 * time.Minute)})
	if err != nil || len(window.Items) != 1 || window.Items[0].SourceID != ids[2] {
		t.Fatalf("event batch did not respect its occurrence-time window: %+v err=%v", window, err)
	}
	private, err := Recall(RecallRequest{Scope: RecallEvents, PersonID: 1, SourceID: ids[3]})
	if err != nil || len(private.Items) != 0 {
		t.Fatalf("private referenced event escaped its branch: %+v err=%v", private, err)
	}
}

// TestRecallSourceIsolationAndIndexRepair exercises the four authoritative
// sources without relying on a model response or a live conversation.
func TestRecallSourceIsolationAndIndexRepair(t *testing.T) {
	db := recallTestDB(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	first := model.Decision{PersonID: 1}
	second := model.Decision{PersonID: 2}
	for _, row := range []*model.Decision{&first, &second} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	ownerAction := model.Action{DecisionID: first.ID, Type: model.ActionTypeChat, Reason: "青丘私有行动", Status: model.ActionStatusEnded, CreatedAt: base}
	foreignAction := model.Action{DecisionID: second.ID, Type: model.ActionTypeChat, Reason: "青丘私有行动", Status: model.ActionStatusEnded, CreatedAt: base}
	ownerWork := model.Work{PersonID: 1, Description: "青丘私有工作", Status: model.WorkStatusRunning, CreatedAt: base}
	foreignWork := model.Work{PersonID: 2, Description: "青丘私有工作", Status: model.WorkStatusRunning, CreatedAt: base}
	ownerHandoff := model.FocusHandoff{PersonID: 1, Summary: "青丘私有交接", CreatedAt: base}
	foreignHandoff := model.FocusHandoff{PersonID: 2, Summary: "青丘私有交接", CreatedAt: base}
	for _, row := range []any{&ownerAction, &foreignAction, &ownerWork, &foreignWork, &ownerHandoff, &foreignHandoff} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := RebuildTermIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct {
		scope   RecallScope
		ownID   int64
		otherID int64
	}{
		{RecallActions, ownerAction.ID, foreignAction.ID},
		{RecallWorks, ownerWork.ID, foreignWork.ID},
		{RecallHandoffs, ownerHandoff.ID, foreignHandoff.ID},
	} {
		page, err := Recall(RecallRequest{Scope: spec.scope, PersonID: 1, Query: "青丘"})
		if err != nil || len(page.Items) != 1 || page.Items[0].SourceID != spec.ownID {
			t.Fatalf("source %d leaked or lost a private row: %+v err=%v", spec.scope, page, err)
		}
		foreign, err := Recall(RecallRequest{Scope: spec.scope, PersonID: 1, SourceID: spec.otherID})
		if err != nil || len(foreign.Items) != 0 {
			t.Fatalf("source %d exposed a foreign exact ID: %+v err=%v", spec.scope, foreign, err)
		}
		within, err := Recall(RecallRequest{Scope: spec.scope, PersonID: 1, SourceID: spec.ownID, FromTime: base, ToTime: base.Add(time.Second)})
		if err != nil || len(within.Items) != 1 {
			t.Fatalf("source %d ignored its inclusive lower bound: %+v err=%v", spec.scope, within, err)
		}
		excluded, err := Recall(RecallRequest{Scope: spec.scope, PersonID: 1, SourceID: spec.ownID, ToTime: base})
		if err != nil || len(excluded.Items) != 0 {
			t.Fatalf("source %d ignored its exclusive upper bound: %+v err=%v", spec.scope, excluded, err)
		}
		batch, err := Recall(RecallRequest{Scope: spec.scope, PersonID: 1, FromTime: base, ToTime: base.Add(time.Second)})
		if err != nil || len(batch.Items) != 1 || batch.Items[0].SourceID != spec.ownID {
			t.Fatalf("source %d batch ignored its time bounds or authorization: %+v err=%v", spec.scope, batch, err)
		}
	}
	// A private Focus handoff has no Work, yet remains readable by its own ID.
	private, err := Recall(RecallRequest{Scope: RecallHandoffs, PersonID: 1, SourceID: ownerHandoff.ID})
	if err != nil || len(private.Items) != 1 || !strings.Contains(private.Items[0].Text, "青丘私有交接") {
		t.Fatalf("private handoff was not directly readable: %+v err=%v", private, err)
	}
	// A missing lexical index cannot block exact-ID or recent source reads.
	if err := db.Where("source_kind = ? AND source_id = ?", model.MemorySourceAction, ownerAction.ID).Delete(&model.MemoryTerm{}).Error; err != nil {
		t.Fatal(err)
	}
	termIndexReady.Store(false)
	missing, err := Recall(RecallRequest{Scope: RecallActions, PersonID: 1, Query: "青丘"})
	if err != nil || len(missing.Items) != 0 || !strings.Contains(missing.Coverage, "incomplete") {
		t.Fatalf("missing index did not report limited coverage: %+v err=%v", missing, err)
	}
	for _, req := range []RecallRequest{
		{Scope: RecallActions, PersonID: 1, SourceID: ownerAction.ID},
		{Scope: RecallActions, PersonID: 1, FromTime: base},
	} {
		page, err := Recall(req)
		if err != nil || len(page.Items) != 1 || page.Items[0].SourceID != ownerAction.ID {
			t.Fatalf("original row disappeared with its index: %+v err=%v", page, err)
		}
	}
	if err := RebuildTermIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	repaired, err := Recall(RecallRequest{Scope: RecallActions, PersonID: 1, Query: "青丘"})
	if err != nil || len(repaired.Items) != 1 || repaired.Items[0].SourceID != ownerAction.ID {
		t.Fatalf("rebuild failed to restore the missing term: %+v err=%v", repaired, err)
	}
	if err := db.Delete(&ownerHandoff).Error; err != nil {
		t.Fatal(err)
	}
	deleted, err := Recall(RecallRequest{Scope: RecallHandoffs, PersonID: 1, Query: "青丘"})
	if err != nil || len(deleted.Items) != 0 {
		t.Fatalf("deleted handoff leaked through stale index: %+v err=%v", deleted, err)
	}
	if err := RebuildTermIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	var stale int64
	if err := db.Model(&model.MemoryTerm{}).Where("source_kind = ? AND source_id = ?", model.MemorySourceFocusHandoff, ownerHandoff.ID).Count(&stale).Error; err != nil || stale != 0 {
		t.Fatalf("rebuild retained deleted source terms: count=%d err=%v", stale, err)
	}
}

// TestRecallKeepsAuthoritativeReadsDuringIndexWriteFailure injects a real
// SQLite write failure after a Work is committed, then verifies recovery.
func TestRecallKeepsAuthoritativeReadsDuringIndexWriteFailure(t *testing.T) {
	db := recallTestDB(t)
	if err := RebuildTermIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	work := model.Work{PersonID: 1, Description: "青丘待检索工作", Status: model.WorkStatusRunning}
	if err := db.Create(&work).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER fail_memory_term_insert BEFORE INSERT ON memory_terms BEGIN SELECT RAISE(FAIL, 'simulated index failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := RefreshSource(model.MemorySourceWork, work.ID); err == nil {
		t.Fatal("index write failure was silently accepted")
	}
	missing, err := Recall(RecallRequest{Scope: RecallWorks, PersonID: 1, Query: "青丘"})
	if err != nil || len(missing.Items) != 0 || !strings.Contains(missing.Coverage, "incomplete") {
		t.Fatalf("failed index write lacked an incomplete-coverage warning: %+v err=%v", missing, err)
	}
	for _, req := range []RecallRequest{
		{Scope: RecallWorks, PersonID: 1, SourceID: work.ID},
		{Scope: RecallWorks, PersonID: 1, FromTime: work.CreatedAt.Add(-time.Second)},
	} {
		page, err := Recall(req)
		if err != nil || len(page.Items) != 1 || page.Items[0].SourceID != work.ID {
			t.Fatalf("index failure hid the authoritative Work: %+v err=%v", page, err)
		}
	}
	if err := db.Exec("DROP TRIGGER fail_memory_term_insert").Error; err != nil {
		t.Fatal(err)
	}
	if err := RebuildTermIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	repaired, err := Recall(RecallRequest{Scope: RecallWorks, PersonID: 1, Query: "青丘"})
	if err != nil || len(repaired.Items) != 1 || repaired.Items[0].SourceID != work.ID || strings.Contains(repaired.Coverage, "incomplete") {
		t.Fatalf("repair did not restore lexical coverage: %+v err=%v", repaired, err)
	}
}

// TestRecallIndexMigratesExistingEvents verifies an old source database can
// gain the rebuildable index without rewriting its existing message or event.
func TestRecallIndexMigratesExistingEvents(t *testing.T) {
	old := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/old.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old })
	for _, row := range []any{&model.Person{}, &model.Session{}, &model.ParticipantSession{}, &model.Message{}, &model.Event{}, &model.AgentObservation{}, &model.Decision{}, &model.Action{}, &model.ActionEffect{}, &model.Work{}, &model.FocusHandoff{}, &model.EntityProfile{}, &model.Jinshu{}, &model.AgentBiography{}, &model.PSDigest{}, &model.ScheduledEvent{}} {
		if err := db.AutoMigrate(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.ParticipantSession{SessionID: 10, ParticipantID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	message := model.Message{SessionID: 10, PersonID: 2, Content: "升级前的青丘消息"}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: 1, EventID: event.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable(&model.MemoryTerm{}) {
		t.Fatal("old fixture unexpectedly contains the new index table")
	}
	if err := db.AutoMigrate(&model.MemoryTerm{}); err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureRecallIndexes(); err != nil {
		t.Fatal(err)
	}
	if err := RebuildTermIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err := Recall(RecallRequest{Scope: RecallMessages, PersonID: 1, Query: "青丘"})
	if err != nil || len(page.Items) != 1 || page.Items[0].SourceID != event.ID {
		t.Fatalf("old observed event was not recalled after migration: %+v err=%v", page, err)
	}
	var foreignKeys []struct{ ID int }
	if err := db.Raw("PRAGMA foreign_key_list(memory_terms)").Scan(&foreignKeys).Error; err != nil || len(foreignKeys) != 0 {
		t.Fatalf("memory_terms has a foreign key: %+v err=%v", foreignKeys, err)
	}
	var columns []struct {
		Name    string `gorm:"column:name"`
		NotNull int    `gorm:"column:notnull"`
	}
	if err := db.Raw("PRAGMA table_info(memory_terms)").Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	for _, column := range columns {
		if column.Name != "id" && column.NotNull != 1 {
			t.Fatalf("memory_terms.%s is nullable", column.Name)
		}
	}
}

// TestRecallCursorDefaultsAndSnapshot verifies the public paging contract
// against rows added after the first page was returned.
func TestRecallCursorDefaultsAndSnapshot(t *testing.T) {
	db := recallTestDB(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	for i := 0; i < 6; i++ {
		row := model.Work{PersonID: 1, Description: "分页任务", CreatedAt: base.Add(time.Duration(i) * time.Minute)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	req := RecallRequest{Scope: RecallWorks, PersonID: 1}
	first, err := Recall(req)
	if err != nil || len(first.Items) != 5 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("default page was not five rows: %+v err=%v", first, err)
	}
	newRow := model.Work{PersonID: 1, Description: "新插入任务"}
	if err := db.Create(&newRow).Error; err != nil {
		t.Fatal(err)
	}
	req.Cursor = first.NextCursor
	second, err := Recall(req)
	if err != nil || len(second.Items) != 1 || second.HasMore || second.Items[0].SourceID == newRow.ID {
		t.Fatalf("new row entered the old cursor chain: %+v err=%v", second, err)
	}
	for _, changed := range []RecallRequest{
		{Scope: RecallWorks, PersonID: 2, Cursor: first.NextCursor},
		{Scope: RecallActions, PersonID: 1, Cursor: first.NextCursor},
		{Scope: RecallWorks, PersonID: 1, Query: "任务", Cursor: first.NextCursor},
		{Scope: RecallWorks, PersonID: 1, FromTime: base, Cursor: first.NextCursor},
		{Scope: RecallWorks, PersonID: 1, ToTime: time.Now(), Cursor: first.NextCursor},
		{Scope: RecallWorks, PersonID: 1, PageSize: 2, Cursor: first.NextCursor},
	} {
		if _, err := Recall(changed); err == nil {
			t.Fatalf("cursor accepted changed scope: %+v", changed)
		}
	}
	if _, err := Recall(RecallRequest{Scope: RecallWorks, PersonID: 1, PageSize: 21}); err == nil {
		t.Fatal("page larger than twenty was accepted")
	}
}

// TestRecallEntityProfileReadsOnlyCurrentOwnerNarrative verifies exact-entity
// lookup and live session membership rather than lexical index behavior.
func TestRecallEntityProfileReadsOnlyCurrentOwnerNarrative(t *testing.T) {
	db := recallTestDB(t)
	if err := db.Create(&model.Person{ID: 2}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Session{ID: 10}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ParticipantSession{SessionID: 10, ParticipantID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	owner := model.EntityProfile{PersonID: 1, EntityType: model.EntityTypePerson, EntityID: 2, Narrative: "当前印象"}
	other := model.EntityProfile{PersonID: 2, EntityType: model.EntityTypePerson, EntityID: 2, Narrative: "他人的私有印象"}
	for _, profile := range []*model.EntityProfile{&owner, &other} {
		if err := db.Create(profile).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, err := RecallEntityProfile(1, model.EntityTypePerson, 2)
	if err != nil || !strings.Contains(got, "当前印象") || strings.Contains(got, "他人的私有印象") {
		t.Fatalf("profile owner isolation failed: %q err=%v", got, err)
	}
	owner.Narrative = "更新后的印象"
	if err := db.Save(&owner).Error; err != nil {
		t.Fatal(err)
	}
	got, err = RecallEntityProfile(1, model.EntityTypePerson, 2)
	if err != nil || !strings.Contains(got, "更新后的印象") || strings.Contains(got, "当前印象") {
		t.Fatalf("profile did not read its current version: %q err=%v", got, err)
	}
	if _, err := RecallEntityProfile(1, model.EntityTypeSession, 10); err != nil {
		t.Fatalf("member could not read session impression: %v", err)
	}
	if err := db.Where("session_id = ? AND participant_id = ?", 10, 1).Delete(&model.ParticipantSession{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := RecallEntityProfile(1, model.EntityTypeSession, 10); err == nil {
		t.Fatal("former member retained session profile access")
	}
}

// TestRecallWorkAndHandoffKeepDistinctTimesAndClaims verifies that a later
// Work update does not masquerade as a new Work or rewrite its completion Event.
func TestRecallWorkAndHandoffKeepDistinctTimesAndClaims(t *testing.T) {
	db := recallTestDB(t)
	created := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	completed := created.Add(time.Hour)
	handedOff := completed.Add(time.Minute)
	work := model.Work{PersonID: 1, SessionID: 25, Description: "整理知识库", Status: model.WorkStatusRunning, CreatedAt: created}
	if err := db.Create(&work).Error; err != nil {
		t.Fatal(err)
	}
	event := model.Event{EventType: model.EventTypeWorkCompleted, RefID: work.ID, CreatedAt: completed}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := CreateObservation(1, event.ID); err != nil {
		t.Fatal(err)
	}
	handoff := model.FocusHandoff{
		PersonID: 1, WorkID: work.ID, SessionID: 25, Status: model.FocusHandoffCompleted,
		Orientation: "原任务", Summary: "交付结果", ConfirmedFindings: "已确认资料", ArtifactReferences: "资料位置", Unresolved: "尚未核对", NextStep: "请查阅", CreatedAt: handedOff,
	}
	if err := db.Create(&handoff).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&work).Updates(map[string]any{"status": model.WorkStatusCompleted, "description": "整理知识库（已完成）", "updated_at": time.Now()}).Error; err != nil {
		t.Fatal(err)
	}

	for _, spec := range []struct {
		scope RecallScope
		id    int64
		at    time.Time
		want  []string
	}{
		{RecallWorks, work.ID, created, []string{"currently has status completed", "整理知识库（已完成）", "It originated in a conversation (session_id=25)."}},
		{RecallEvents, event.ID, completed, []string{"completion event", "current status"}},
		{RecallHandoffs, handoff.ID, handedOff, []string{"That work originated in a conversation (session_id=25).", "交付结果", "已确认资料", "资料位置", "尚未核对", "请查阅"}},
	} {
		page, err := Recall(RecallRequest{Scope: spec.scope, PersonID: 1, SourceID: spec.id})
		if err != nil || len(page.Items) != 1 || !page.Items[0].OccurredAt.Equal(spec.at) {
			t.Fatalf("source %d lost its occurrence time: %+v err=%v", spec.scope, page, err)
		}
		for _, phrase := range spec.want {
			if !strings.Contains(page.Items[0].Text, phrase) {
				t.Fatalf("source %d omitted %q: %+v", spec.scope, phrase, page.Items[0])
			}
		}
		if strings.Contains(page.Items[0].Text, fmt.Sprintf("(work_id=%d, session_id=25)", work.ID)) {
			t.Fatalf("the source conversation was presented as part of the work ID: %+v", page.Items[0])
		}
		if spec.scope == RecallEvents && strings.Contains(page.Items[0].Text, handoff.Summary) {
			t.Fatalf("later handoff rewrote the earlier completion Event: %+v", page.Items[0])
		}
		outside, err := Recall(RecallRequest{Scope: spec.scope, PersonID: 1, SourceID: spec.id, FromTime: spec.at.Add(time.Second)})
		if err != nil || len(outside.Items) != 0 {
			t.Fatalf("source %d appeared after its creation: %+v err=%v", spec.scope, outside, err)
		}
	}
	linked, err := Recall(RecallRequest{Scope: RecallHandoffs, PersonID: 1, WorkID: work.ID})
	if err != nil || len(linked.Items) != 1 || linked.Items[0].SourceID != handoff.ID {
		t.Fatalf("work handoff was not found by work ID: %+v err=%v", linked, err)
	}
	longWork := model.Work{PersonID: 1, Description: strings.Repeat("青", 610)}
	if err := db.Create(&longWork).Error; err != nil {
		t.Fatal(err)
	}
	shortened, err := Recall(RecallRequest{Scope: RecallWorks, PersonID: 1, SourceID: longWork.ID})
	if err != nil || len(shortened.Items) != 1 || shortened.HasMore || !strings.Contains(shortened.Items[0].Text, "[text abbreviated; original source has more]") {
		t.Fatalf("text abbreviation was confused with result pagination: %+v err=%v", shortened, err)
	}
}
