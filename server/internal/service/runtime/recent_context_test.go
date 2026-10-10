package runtime

import (
	"strconv"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

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
	for _, wanted := range []string{"停止重复检查和发送", "文件已确认存在", "cancel focused work Work #", "no accepted control recorded"} {
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
	for _, wanted := range []string{"停止重复检查和发送", "Cancel Work #", "no accepted control recorded"} {
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
