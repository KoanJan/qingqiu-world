package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/energy"
)

func TestPersonHistoryUsesExplicitDyadAndCausalWork(t *testing.T) {
	db := recallTestDB(t)
	for _, p := range []model.Person{{ID: 1, Name: "Self"}, {ID: 2, Name: "Other"}, {ID: 3, Name: "Third"}} {
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{10, 11} {
		if err := db.Create(&model.Session{ID: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []model.ParticipantSession{{SessionID: 10, ParticipantID: 1}, {SessionID: 10, ParticipantID: 2}, {SessionID: 11, ParticipantID: 1}, {SessionID: 11, ParticipantID: 3}} {
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().Add(-time.Hour)
	var relatedEventID int64
	for _, spec := range []struct {
		session, speaker int64
		text             string
	}{{10, 2, "Our plan"}, {11, 3, "Private third-party plan"}} {
		msg := model.Message{SessionID: spec.session, PersonID: spec.speaker, Content: spec.text, CreatedAt: base}
		if err := db.Create(&msg).Error; err != nil {
			t.Fatal(err)
		}
		ev := model.Event{EventType: model.EventTypeMessage, RefID: msg.ID, CreatedAt: base}
		if err := db.Create(&ev).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.AgentObservation{PersonID: 1, EventID: ev.ID, CreatedAt: base}).Error; err != nil {
			t.Fatal(err)
		}
		if spec.session == 10 {
			relatedEventID = ev.ID
		}
	}
	d := model.Decision{PersonID: 1, EventID: relatedEventID, CreatedAt: base.Add(time.Minute)}
	if err := db.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	a := model.Action{DecisionID: d.ID, Type: model.ActionTypeStartFocusedWork, Status: model.ActionStatusEnded, CreatedAt: base.Add(2 * time.Minute)}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	w := model.Work{PersonID: 1, Description: "Follow our plan", CreatedAt: base.Add(3 * time.Minute)}
	if err := db.Create(&w).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ActionEffect{ActionID: a.ID, EffectType: model.ActionEffectWork, EffectID: w.ID}).Error; err != nil {
		t.Fatal(err)
	}
	page, err := Recall(RecallRequest{Scope: RecallAll, PersonID: 1, CounterpartID: 2, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("expected dyad message, causal action, work; got %+v", page.Items)
	}
	for _, item := range page.Items {
		if strings.Contains(item.Text, "Third") {
			t.Fatalf("unrelated source leaked: %+v", item)
		}
	}
	if _, err := Recall(RecallRequest{Scope: RecallAll, PersonID: 3, CounterpartID: 2, PageSize: 10}); err != nil {
		t.Fatal(err)
	}
}

func TestDailySourceContactTimeAndOwnership(t *testing.T) {
	db := recallTestDB(t)
	for _, p := range []model.Person{{ID: 1, Name: "Self"}, {ID: 2, Name: "Other"}} {
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.Session{ID: 10}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2} {
		if err := db.Create(&model.ParticipantSession{SessionID: 10, ParticipantID: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	message := model.Message{SessionID: 10, PersonID: 2, Content: "Yesterday's news", CreatedAt: time.Now().Add(-48 * time.Hour)}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID, CreatedAt: message.CreatedAt}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	contact := time.Now().In(energy.Timezone()).AddDate(0, 0, -1).Truncate(time.Second)
	obs := model.AgentObservation{PersonID: 1, EventID: event.ID, CreatedAt: contact}
	if err := db.Create(&obs).Error; err != nil {
		t.Fatal(err)
	}
	shard := model.DailyMemory{PersonID: 1, LocalDate: contact.In(energy.Timezone()).Format("2006-01-02"), Shard: 0, GroupType: model.DailyMemoryGroupChat, GroupID: 10, Summary: "Late contact", StartAt: contact, EndAt: contact, Embedding: []byte{}}
	if err := db.Create(&shard).Error; err != nil {
		t.Fatal(err)
	}
	groups, err := dailyGroupsForDate(context.Background(), 1, shard.LocalDate)
	if err != nil || len(groups) != 1 || groups[0].Key != (dailyGroupKey{Type: model.DailyMemoryGroupChat, ID: 10}) {
		t.Fatalf("date=%s contact=%s observation=%s groups=%+v err=%v", shard.LocalDate, contact, obs.CreatedAt, groups, err)
	}
	items, more, _, err := ReadDailyMemorySources(context.Background(), 1, shard.ID, 5, 0)
	if err != nil || more || len(items) != 1 || items[0].EventID != event.ID || !items[0].ContactAt.Equal(contact) {
		t.Fatalf("sources=%+v more=%v err=%v", items, more, err)
	}
	if _, _, _, err := ReadDailyMemorySources(context.Background(), 2, shard.ID, 5, 0); err == nil {
		t.Fatal("another person read a private date index")
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, _, _, err := ReadDailyMemorySources(canceled, 1, shard.ID, 5, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled source expansion continued: %v", err)
	}
	page, err := RecallDailyMemories(context.Background(), 1, 0, "", time.Time{}, time.Time{}, 5, 0)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("date page=%+v err=%v", page, err)
	}
	openDay := time.Now().In(energy.Timezone())
	openIndex := model.DailyMemory{PersonID: 1, LocalDate: openDay.Format("2006-01-02"), GroupType: model.DailyMemoryGroupChat, GroupID: 10, Summary: "Partial day", StartAt: openDay, EndAt: openDay, Embedding: []byte{}}
	if err := db.Create(&openIndex).Error; err != nil {
		t.Fatal(err)
	}
	if page, err := RecallDailyMemories(context.Background(), 1, 0, "", time.Time{}, time.Time{}, 5, 0); err != nil || len(page.Items) != 1 {
		t.Fatalf("open date index became visible: page=%+v err=%v", page, err)
	}
	if _, _, _, err := ReadDailyMemorySources(context.Background(), 1, openIndex.ID, 5, 0); err == nil {
		t.Fatal("open date index was readable by ID")
	}
	if page, err := RecallDailyMemories(context.Background(), 1, 0, "", time.Time{}, time.Time{}, 5, 10001); err != nil || len(page.Items) != 0 {
		t.Fatalf("large valid page offset was rejected: page=%+v err=%v", page, err)
	}
	if _, err := RecallDailyMemories(context.Background(), 1, -1, "", time.Time{}, time.Time{}, 5, 0); err == nil {
		t.Fatal("negative memory_id silently became a browse request")
	}
	if items, more, _, err := ReadDailyMemorySources(context.Background(), 1, shard.ID, 5, 10001); err != nil || more || len(items) != 0 {
		t.Fatalf("large valid source offset was rejected: items=%+v more=%v err=%v", items, more, err)
	}
}

func TestRecentDailyIndexSeedOnlyBackfillsSevenLocalDates(t *testing.T) {
	db := recallTestDB(t)
	cutoff := recentDailyBackfillStart(time.Now())
	old := cutoff.Add(-time.Hour)
	recent := cutoff.Add(time.Hour)
	today := cutoff.AddDate(0, 0, 7).Add(time.Hour)
	future := cutoff.AddDate(0, 0, 8).Add(time.Hour)
	for _, at := range []time.Time{old, recent, today, future} {
		event := model.Event{EventType: model.EventTypeSystemNotification, PayloadJSON: `"test"`, CreatedAt: at}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.AgentObservation{PersonID: 1, EventID: event.ID, CreatedAt: at}).Error; err != nil {
			t.Fatal(err)
		}
		if at.Equal(old) {
			decision := model.Decision{PersonID: 1, EventID: event.ID, CreatedAt: old}
			if err := db.Create(&decision).Error; err != nil {
				t.Fatal(err)
			}
			// A recently persisted action still belongs to the old observation day.
			if err := db.Create(&model.Action{DecisionID: decision.ID, Type: model.ActionTypeChat, CreatedAt: recent}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, at := range []time.Time{old, recent, today, future} {
		decision := model.Decision{PersonID: 1, CreatedAt: at}
		if err := db.Create(&decision).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.Action{DecisionID: decision.ID, Type: model.ActionTypeChat, CreatedAt: at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	oldIndex := model.DailyMemory{
		PersonID: 1, LocalDate: old.In(energy.Timezone()).Format("2006-01-02"),
		GroupType: model.DailyMemoryGroupSystemNotice, GroupID: 0,
		Summary: "Old summary", StartAt: old, EndAt: old,
		Embedding: []byte{},
	}
	if err := db.Create(&oldIndex).Error; err != nil {
		t.Fatal(err)
	}
	state := dailyIndexState{}
	if err := seedRecentDailyIndex(context.Background(), &state); err != nil {
		t.Fatal(err)
	}
	wantDate := recent.In(energy.Timezone()).Format("2006-01-02")
	todayDate := today.In(energy.Timezone()).Format("2006-01-02")
	if !state.initialized || len(state.pending) != 1 || len(state.pending[1]) != 2 {
		t.Fatalf("unexpected backfill dates: %+v", state.pending)
	}
	if _, ok := state.pending[1][wantDate]; !ok {
		t.Fatalf("recent contact date %s missing: %+v", wantDate, state.pending)
	}
	if _, ok := state.pending[1][todayDate]; !ok {
		t.Fatalf("open contact date %s was not queued for tomorrow: %+v", todayDate, state.pending)
	}
	var lastObservationID, lastDecisionID int64
	if err := db.Model(&model.AgentObservation{}).Select("COALESCE(MAX(id),0)").Scan(&lastObservationID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Decision{}).Select("COALESCE(MAX(id),0)").Scan(&lastDecisionID).Error; err != nil {
		t.Fatal(err)
	}
	if state.lastObservationID != lastObservationID || state.lastDecisionID != lastDecisionID {
		t.Fatalf("incremental cursors did not skip preexisting history: %+v", state)
	}
}

func TestDailyIndexDefersOpenLocalDate(t *testing.T) {
	db := recallTestDB(t)
	now := time.Now().In(energy.Timezone())
	event := model.Event{EventType: model.EventTypeSystemNotification, PayloadJSON: `"today"`, CreatedAt: now}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: 1, EventID: event.ID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	state := dailyIndexState{}
	if err := refreshDailyMemoryIndex(context.Background(), &state); err != nil {
		t.Fatal(err)
	}
	date := now.Format("2006-01-02")
	if _, queued := state.pending[1][date]; !queued {
		t.Fatalf("open date %s was not retained for the next day: %+v", date, state.pending)
	}
	var count int64
	if err := db.Model(&model.DailyMemory{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("open date generated %d summaries", count)
	}
}

// TestDailyIndexDiscoversNewDecisionContactDate verifies that a Decision alone
// opens a contact date, including a heartbeat that produced no Action.
func TestDailyIndexDiscoversNewDecisionContactDate(t *testing.T) {
	db := recallTestDB(t)
	now := time.Now().In(energy.Timezone())
	decision := model.Decision{PersonID: 1, CreatedAt: now, Thoughts: "nothing needs doing"}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	state := dailyIndexState{initialized: true, pending: make(map[int64]map[string]struct{})}
	if err := refreshDailyMemoryIndex(context.Background(), &state); err != nil {
		t.Fatal(err)
	}
	date := now.Format("2006-01-02")
	if state.lastDecisionID != decision.ID {
		t.Fatalf("new decision cursor did not advance: %+v", state)
	}
	if _, queued := state.pending[1][date]; !queued {
		t.Fatalf("new decision did not queue contact date %s: %+v", date, state.pending)
	}
}

// TestDailyIndexDatesDecisionByItsOwnDay verifies that an Action persisted after
// its Decision still queues the Decision's local date. The Action's later
// persistence date cannot define the day because a closed day is immutable.
func TestDailyIndexDatesDecisionByItsOwnDay(t *testing.T) {
	db := recallTestDB(t)
	now := time.Now().In(energy.Timezone())
	state := dailyIndexState{}
	if err := seedRecentDailyIndex(context.Background(), &state); err != nil {
		t.Fatal(err)
	}
	state.pending = make(map[int64]map[string]struct{})
	decisionDate := now.AddDate(0, 0, -1).Truncate(time.Second)
	decision := model.Decision{PersonID: 1, CreatedAt: decisionDate}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	action := model.Action{DecisionID: decision.ID, Type: model.ActionTypeChat, CreatedAt: now}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	if err := refreshDailyMemoryIndex(context.Background(), &state); err != nil {
		t.Fatal(err)
	}
	if state.lastDecisionID != decision.ID {
		t.Fatalf("decision cursor=%d, want %d", state.lastDecisionID, decision.ID)
	}
	date := decisionDate.In(energy.Timezone()).Format("2006-01-02")
	if _, ok := state.pending[1][date]; !ok {
		t.Fatalf("decision did not queue its own date %s: %+v", date, state.pending)
	}
	if _, ok := state.pending[1][now.Format("2006-01-02")]; ok {
		t.Fatalf("decision queued its action's persistence date instead: %+v", state.pending)
	}
}

func TestPersonProfileEvidenceIncludesBothSpeakersWithoutChangingTrigger(t *testing.T) {
	db := recallTestDB(t)
	for _, p := range []model.Person{{ID: 1, Name: "Self"}, {ID: 2, Name: "Other"}} {
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.Session{ID: 10}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2} {
		if err := db.Create(&model.ParticipantSession{SessionID: 10, ParticipantID: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var observations []model.AgentObservation
	for _, spec := range []struct {
		personID int64
		text     string
	}{{2, "Could we meet?"}, {1, "Yes, we can."}} {
		message := model.Message{SessionID: 10, PersonID: spec.personID, Content: spec.text}
		if err := db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		observation := model.AgentObservation{PersonID: 1, EventID: event.ID}
		if err := db.Create(&observation).Error; err != nil {
			t.Fatal(err)
		}
		observations = append(observations, observation)
	}
	letter := model.Jinshu{FromPersonID: 2, ToPersonID: 1, Topic: "Weekend", Description: "I can meet Saturday."}
	if err := db.Create(&letter).Error; err != nil {
		t.Fatal(err)
	}
	letterEvent := model.Event{EventType: model.EventTypeJinshu, RefID: letter.ID}
	if err := db.Create(&letterEvent).Error; err != nil {
		t.Fatal(err)
	}
	letterObservation := model.AgentObservation{PersonID: 1, EventID: letterEvent.ID}
	if err := db.Create(&letterObservation).Error; err != nil {
		t.Fatal(err)
	}
	observations = append(observations, letterObservation)
	directions := resolveEntityDirections(observations)
	if got := directions[entityDirection{EntityType: model.EntityTypePerson, EntityID: 2}]; got != 1 {
		t.Fatalf("own reply changed counterpart trigger count: %d", got)
	}
	evidence := loadProfileEvidences(1, model.EntityTypePerson, 2, "Self")
	joined := strings.Join(evidence, "\n")
	if !strings.Contains(joined, "Other") || !strings.Contains(joined, "Could we meet?") || !strings.Contains(joined, "Self") || !strings.Contains(joined, "Yes, we can.") || !strings.Contains(joined, "I can meet Saturday.") {
		t.Fatalf("profile omitted one side: %s", joined)
	}
}

func TestPersonProfileEvidenceKeepsImportanceOrdering(t *testing.T) {
	db := recallTestDB(t)
	for _, person := range []model.Person{{ID: 1, Name: "Self"}, {ID: 2, Name: "Other"}} {
		if err := db.Create(&person).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i <= profileTopK; i++ {
		topic := fmt.Sprintf("old-%d", i)
		importance := 0.8
		if i == profileTopK {
			topic = "recent-low"
			importance = 0.5
		}
		letter := model.Jinshu{FromPersonID: 2, ToPersonID: 1, Topic: topic, Description: topic}
		if err := db.Create(&letter).Error; err != nil {
			t.Fatal(err)
		}
		event := model.Event{EventType: model.EventTypeJinshu, RefID: letter.ID}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		observation := model.AgentObservation{PersonID: 1, EventID: event.ID, Importance: importance}
		if err := db.Create(&observation).Error; err != nil {
			t.Fatal(err)
		}
	}
	evidence := loadProfileEvidences(1, model.EntityTypePerson, 2, "Self")
	joined := strings.Join(evidence, "\n")
	if len(evidence) != profileTopK || strings.Contains(joined, "recent-low") || !strings.Contains(joined, "old-0") {
		t.Fatalf("profile selection bypassed importance ordering: count=%d evidence=%s", len(evidence), joined)
	}
}

func TestDailyGroupKeepsObservationDecisionAndOwnAction(t *testing.T) {
	db := recallTestDB(t)
	now := time.Now().In(energy.Timezone())
	base := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, energy.Timezone()).AddDate(0, 0, -1)
	for _, p := range []model.Person{{ID: 1, Name: "Self"}, {ID: 2, Name: "Other"}} {
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
	}
	ev := model.Event{EventType: model.EventTypeSystemNotification, RefID: 0, PayloadJSON: `"news"`, CreatedAt: base}
	if err := db.Create(&ev).Error; err != nil {
		t.Fatal(err)
	}
	obs := model.AgentObservation{PersonID: 1, EventID: ev.ID, CreatedAt: base}
	if err := db.Create(&obs).Error; err != nil {
		t.Fatal(err)
	}
	d := model.Decision{PersonID: 1, EventID: ev.ID, CreatedAt: base.Add(time.Minute)}
	if err := db.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	a := model.Action{DecisionID: d.ID, Type: model.ActionTypeChat, Status: model.ActionStatusEnded, CreatedAt: base.Add(time.Minute)}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	date := base.In(energy.Timezone()).Format("2006-01-02")
	groups, err := dailyGroupsForDate(context.Background(), 1, date)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	group := groups[0]
	if group.Key != (dailyGroupKey{Type: model.DailyMemoryGroupSystemNotice, ID: 0}) || len(group.Sources) != 2 || group.Sources[0].ID != obs.ID || len(group.Decisions) != 1 || group.Decisions[0].Record.ID != d.ID || len(group.Decisions[0].Actions) != 1 || group.Decisions[0].Actions[0].ID != a.ID {
		t.Fatalf("causal group=%+v", group)
	}
	keys := append([]dailySource(nil), group.Sources...)
	if err := fillDailySourceText(context.Background(), 1, group.DayStart, group.DayEnd, keys); err != nil {
		t.Fatal(err)
	}
	if keys[0].RefID != ev.ID || !strings.Contains(keys[1].Text, "intention") {
		t.Fatalf("source attribution=%+v", keys)
	}
	shard := model.DailyMemory{PersonID: 1, LocalDate: date, Shard: 0, GroupType: group.Key.Type, GroupID: group.Key.ID, Summary: "Existing account", StartAt: group.StartAt, EndAt: group.EndAt, Embedding: []byte{}}
	if err := db.Create(&shard).Error; err != nil {
		t.Fatal(err)
	}
	first, more, _, err := ReadDailyMemorySources(context.Background(), 1, shard.ID, 1, 0)
	if err != nil || !more || len(first) != 1 || first[0].EventID != ev.ID {
		t.Fatalf("first mixed source page=%+v more=%v err=%v", first, more, err)
	}
	second, more, _, err := ReadDailyMemorySources(context.Background(), 1, shard.ID, 1, 1)
	if err != nil || more || len(second) != 1 || second[0].ActionID != a.ID {
		t.Fatalf("second mixed source page=%+v more=%v err=%v", second, more, err)
	}
	remaining := 0
	complete, err := refreshDailyMemoryDay(context.Background(), 1, date, &remaining)
	if err != nil || !complete {
		t.Fatalf("unchanged shard was not idempotent: complete=%v err=%v", complete, err)
	}
	later := model.AgentObservation{PersonID: 1, EventID: ev.ID, CreatedAt: base.Add(2 * time.Minute)}
	// A second Event is needed because observations are unique by owner and event.
	secondEvent := model.Event{EventType: model.EventTypeSystemNotification, PayloadJSON: `"later"`, CreatedAt: later.CreatedAt}
	if err := db.Create(&secondEvent).Error; err != nil {
		t.Fatal(err)
	}
	later.EventID = secondEvent.ID
	if err := db.Create(&later).Error; err != nil {
		t.Fatal(err)
	}
	complete, err = refreshDailyMemoryDay(context.Background(), 1, date, &remaining)
	if err != nil || !complete || remaining != 0 {
		t.Fatalf("completed day was not append-only: complete=%v remaining=%d err=%v", complete, remaining, err)
	}
	var stored []model.DailyMemory
	if err := db.Where("person_id = ? AND local_date = ?", 1, date).Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].ID != shard.ID || stored[0].Summary != "Existing account" {
		t.Fatalf("a completed day was rewritten: %+v", stored)
	}
}

func TestDailyGroupsKeepLargeConversationAndHeartbeatDecisionTogether(t *testing.T) {
	db := recallTestDB(t)
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, energy.Timezone())
	if err := db.Create(&model.Session{ID: 30}).Error; err != nil {
		t.Fatal(err)
	}
	var firstEventID int64
	for i := 0; i < 25; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		message := model.Message{SessionID: 30, PersonID: 2, Content: fmt.Sprintf("Message %d", i), CreatedAt: at}
		if err := db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID, CreatedAt: at}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.AgentObservation{PersonID: 1, EventID: event.ID, CreatedAt: at}).Error; err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstEventID = event.ID
		}
	}
	triggered := model.Decision{PersonID: 1, EventID: firstEventID, CreatedAt: base.Add(time.Second)}
	// Two separate heartbeats on the same day share one day-level group.
	firstBeat := model.Decision{PersonID: 1, CreatedAt: base.Add(30 * time.Minute)}
	secondBeat := model.Decision{PersonID: 1, CreatedAt: base.Add(45 * time.Minute)}
	for _, decision := range []*model.Decision{&triggered, &firstBeat, &secondBeat} {
		if err := db.Create(decision).Error; err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := db.Create(&model.Action{DecisionID: decision.ID, Type: model.ActionTypeChat, Background: "A prior request", Reason: fmt.Sprintf("Reason %d", i), CreatedAt: decision.CreatedAt}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	groups, err := dailyGroupsForDate(context.Background(), 1, base.Format("2006-01-02"))
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	byType := make(map[model.DailyMemoryGroupType]*dailyGroup)
	for _, group := range groups {
		byType[group.Key.Type] = group
	}
	conversation := byType[model.DailyMemoryGroupChat]
	if conversation == nil || conversation.Key.ID != 30 || len(conversation.Observations) != 25 || len(conversation.Sources) != 27 || len(conversation.Decisions) != 1 || len(conversation.Decisions[0].Actions) != 2 {
		t.Fatalf("conversation split or causal actions lost: %+v", conversation)
	}
	beat := byType[model.DailyMemoryGroupHeartbeat]
	if beat == nil || beat.Key.ID != 0 || len(beat.Sources) != 4 || len(beat.Decisions) != 2 || len(beat.Decisions[0].Actions) != 2 || len(beat.Decisions[1].Actions) != 2 {
		t.Fatalf("heartbeat decisions did not share one day group: %+v", beat)
	}
}

func TestDailyMemorySearchReachesBeyondPreviousCandidateWindow(t *testing.T) {
	db := recallTestDB(t)
	previousEmbedding := embeddingSvc
	embeddingSvc = nil
	t.Cleanup(func() { embeddingSvc = previousEmbedding })
	local := time.Now().In(energy.Timezone())
	base := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, energy.Timezone()).AddDate(0, 0, -1)
	rows := make([]model.DailyMemory, 2002)
	for i := range rows {
		at := base.Add(time.Duration(i) * time.Second)
		summary := "ordinary date group"
		if i == 0 {
			summary = "unique-old-memory"
		}
		rows[i] = model.DailyMemory{
			PersonID: 1, LocalDate: base.Format("2006-01-02"), Shard: i,
			GroupType: model.DailyMemoryGroupWork, GroupID: int64(i + 1), Summary: summary, StartAt: at, EndAt: at,
			Embedding: []byte{},
		}
	}
	if err := db.CreateInBatches(&rows, 40).Error; err != nil {
		t.Fatal(err)
	}
	page, err := RecallDailyMemories(context.Background(), 1, 0, "unique-old-memory", time.Time{}, time.Time{}, 5, 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != rows[0].ID {
		t.Fatalf("older group was excluded: page=%+v err=%v", page, err)
	}
}
