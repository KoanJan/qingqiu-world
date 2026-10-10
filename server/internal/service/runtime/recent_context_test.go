package runtime

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// TestDecideContextKeepsSpeakersAndReferencesDistinct checks the two actual
// Decide inputs involved in a remembered conversation: the Situation summary
// and the serialized recall_message result.
func TestDecideContextKeepsSpeakersAndReferencesDistinct(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/speaker-context.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.Session{}, &model.ParticipantSession{},
		&model.Message{}, &model.Event{}, &model.AgentObservation{}, &model.Decision{},
		&model.Action{}, &model.ActionEffect{}); err != nil {
		t.Fatal(err)
	}
	oldDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = oldDB })
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
	var ownMessageID int64
	for _, spec := range []struct {
		personID int64
		content  string
	}{{1, "你好呀粒粒"}, {13, "嗯"}} {
		message := model.Message{PersonID: spec.personID, SessionID: 25, Content: spec.content}
		if err := db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		event := model.Event{EventType: model.EventTypeMessage, RefID: message.ID, CreatedAt: message.CreatedAt}
		if err := db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.AgentObservation{PersonID: 13, EventID: event.ID}).Error; err != nil {
			t.Fatal(err)
		}
		if spec.personID == 13 {
			ownMessageID = message.ID
		}
	}
	decision := model.Decision{PersonID: 13}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	action := model.Action{DecisionID: decision.ID, Type: model.ActionTypeChat, Status: model.ActionStatusEnded}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ActionEffect{ActionID: action.ID, EffectType: model.ActionEffectMessage, EffectID: ownMessageID}).Error; err != nil {
		t.Fatal(err)
	}

	summary := formatGeneralSubject(SituationSubject{RecentExperienceSummary: buildRecentExperienceSummary(13, 0)})
	for _, wanted := range []string{"Chat message from session (session_id=25) — Patrick said: \"你好呀粒粒\".", "Chat message from session (session_id=25) — You said: \"嗯\"."} {
		if !strings.Contains(summary, wanted) {
			t.Fatalf("Decide summary omits speaker %q: %s", wanted, summary)
		}
	}
	output, err := executeRecallTool(13, 0, "recall_message", `{"session_id":25}`)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []struct {
			EventID   int64  `json:"event_id"`
			MessageID int64  `json:"message_id"`
			Text      string `json:"text"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(output), &page); err != nil {
		t.Fatal(err)
	}
	joined := summary + output
	for _, wanted := range []string{"Chat message from session (session_id=25) — You said: \"嗯\". This record resulted from your action (action_id="} {
		if len(page.Items) == 0 || !strings.Contains(page.Items[0].Text, wanted) {
			t.Fatalf("Decide recall omits %q: %s", wanted, output)
		}
	}
	if len(page.Items) != 2 || page.Items[0].EventID <= 0 || page.Items[0].MessageID <= 0 ||
		strings.Contains(joined, "source_kind") || strings.Contains(joined, "source_id") ||
		strings.Contains(joined, "Person 13 said") || strings.Contains(joined, "your action_id=") {
		t.Fatalf("Decide mixed speaker identity with record syntax: %s", joined)
	}
}

// TestRecentExperienceKeepsStoppedWorkIntent checks the real race boundary:
// a completed Work's next Decision must see an already-ended Cancel attempt.
func TestRecentExperienceKeepsStoppedWorkIntent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/recent-context.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.Session{}, &model.ParticipantSession{},
		&model.Message{}, &model.Event{}, &model.AgentObservation{}, &model.Decision{},
		&model.Action{}, &model.ActionEffect{}, &model.Work{}, &model.FocusHandoff{}); err != nil {
		t.Fatal(err)
	}
	oldDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = oldDB })
	const selfID int64 = 12
	for _, person := range []model.Person{{ID: selfID, Name: "Agent"}, {ID: 1, Name: "Patrick"}, {ID: 13, Name: "Other"}} {
		if err := db.Create(&person).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.Session{ID: 24, Title: "Game"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ParticipantSession{SessionID: 24, ParticipantID: selfID}).Error; err != nil {
		t.Fatal(err)
	}
	work := model.Work{PersonID: selfID, Description: "Check game files", Status: model.WorkStatusCompleted}
	if err := db.Create(&work).Error; err != nil {
		t.Fatal(err)
	}
	stopMessage := model.Message{SessionID: 24, PersonID: 1, Content: "停止重复检查和发送"}
	if err := db.Create(&stopMessage).Error; err != nil {
		t.Fatal(err)
	}
	stopEvent := model.Event{EventType: model.EventTypeMessage, RefID: stopMessage.ID}
	if err := db.Create(&stopEvent).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: selfID, EventID: stopEvent.ID}).Error; err != nil {
		t.Fatal(err)
	}
	priorWork := model.Work{PersonID: selfID, Description: "Check prior files", Status: model.WorkStatusCompleted}
	if err := db.Create(&priorWork).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.FocusHandoff{PersonID: selfID, WorkID: priorWork.ID, Summary: "文件已确认存在"}).Error; err != nil {
		t.Fatal(err)
	}
	priorResult := model.Event{EventType: model.EventTypeWorkCompleted, RefID: priorWork.ID}
	if err := db.Create(&priorResult).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: selfID, EventID: priorResult.ID}).Error; err != nil {
		t.Fatal(err)
	}
	decision := model.Decision{PersonID: selfID, EventID: stopEvent.ID}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	cancel := model.Action{DecisionID: decision.ID, Type: model.ActionTypeCancelFocusedWork,
		PlanJSON: `{"target_work_id":` + workIDText(work.ID) + `,"guidance":"stop"}`,
		Reason:   "Patrick asked me to stop", Status: model.ActionStatusEnded}
	if err := db.Create(&cancel).Error; err != nil {
		t.Fatal(err)
	}
	current := model.Event{EventType: model.EventTypeWorkCompleted, RefID: work.ID}
	if err := db.Create(&current).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: selfID, EventID: current.ID}).Error; err != nil {
		t.Fatal(err)
	}
	privateMessage := model.Message{SessionID: 24, PersonID: 13, Content: "unobserved private content"}
	if err := db.Create(&privateMessage).Error; err != nil {
		t.Fatal(err)
	}
	privateEvent := model.Event{EventType: model.EventTypeMessage, RefID: privateMessage.ID}
	if err := db.Create(&privateEvent).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentObservation{PersonID: 13, EventID: privateEvent.ID}).Error; err != nil {
		t.Fatal(err)
	}

	recent := buildRecentExperienceSummary(selfID, current.ID)
	for _, wanted := range []string{"停止重复检查和发送", "文件已确认存在", "cancel focused work (work_id=", "no accepted control was recorded"} {
		if !strings.Contains(recent, wanted) {
			t.Fatalf("recent experience omits %q: %s", wanted, recent)
		}
	}
	for _, forbidden := range []string{"unobserved private content", "Work 1 execution ended"} {
		if strings.Contains(recent, forbidden) {
			t.Fatalf("recent experience included %q: %s", forbidden, recent)
		}
	}
	controls := buildWorkControlContext(selfID, work.ID)
	for _, wanted := range []string{"停止重复检查和发送", "attempted to cancel the work (work_id=", "no accepted control was recorded"} {
		if !strings.Contains(controls, wanted) {
			t.Fatalf("control context omits %q: %s", wanted, controls)
		}
	}
	if other := buildWorkControlContext(13, work.ID); other != "" {
		t.Fatalf("another person read Work controls: %s", other)
	}
}

// workIDText formats a test Work ID for a persisted JSON plan.
func workIDText(id int64) string { return strconv.FormatInt(id, 10) }
