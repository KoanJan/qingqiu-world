package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// TestGeneralSituationReportsOmittedCounts checks every bounded general
// overview without borrowing the current event's specific history.
func TestGeneralSituationReportsOmittedCounts(t *testing.T) {
	old := database.DB
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/situation.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.Session{}, &model.ParticipantSession{}, &model.AgentObservation{}, &model.Decision{}, &model.Action{}, &model.Work{}); err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = old })
	const self int64 = 991001
	settings := config.Get()
	oldAOSRoot := settings.AOSRoot
	settings.AOSRoot = filepath.Join(t.TempDir(), "aos")
	t.Cleanup(func() { settings.AOSRoot = oldAOSRoot })
	if err := os.MkdirAll(filepath.Join(settings.AOSRoot, fmt.Sprint(self), "private"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Person{ID: self, Name: "Self", Type: model.PersonTypeAI}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 22; i++ {
		peerID := int64(992000 + i)
		if err := db.Create(&model.Person{ID: peerID, Name: fmt.Sprintf("Peer %d", i), Type: model.PersonTypeHuman}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.ParticipantSession{SessionID: int64(993000 + i), ParticipantID: self}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.Session{ID: int64(993000 + i), Title: "History"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	decision := model.Decision{PersonID: self}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if err := db.Create(&model.Action{DecisionID: decision.ID, Type: model.ActionTypeChat,
			Status: model.ActionStatusInProgress, Background: "pending", Reason: "pending"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.Work{PersonID: self, SessionID: 993000, Description: "pending", Status: model.WorkStatusRunning}).Error; err != nil {
			t.Fatal(err)
		}
	}
	situation := &Situation{}
	populateGeneralSituation(self, situation)
	if !strings.Contains(situation.Environment.Sessions, "Conversation (session_id=") || strings.Contains(situation.Environment.Sessions, "With no other participants (session_id=") {
		t.Fatalf("conversation ID was attached to participants: %q", situation.Environment.Sessions)
	}
	for _, item := range []struct {
		label, value, omitted string
	}{
		{"actions", situation.Subject.ActiveActionsSummary, "2 further ongoing actions"},
		{"works", situation.Subject.ActiveWorksSummary, "2 further active works"},
		{"sessions", situation.Environment.Sessions, "2 further sessions not shown"},
		{"persons", situation.Environment.Persons, "2 further contactable persons are not shown"},
	} {
		if !strings.Contains(item.value, item.omitted) {
			t.Errorf("%s omitted count missing from %q", item.label, item.value)
		}
	}
	dir := t.TempDir()
	for i := 0; i < 22; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("item-%02d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	resources, err := readDirSummary(dir)
	if err != nil || !strings.Contains(resources, "2 more entries not shown") {
		t.Fatalf("resource overview did not report omitted entries: %q err=%v", resources, err)
	}
}

// TestOngoingActionSummaryUsesPlanMeaning checks that storage JSON does not
// become raw prompt material when an asynchronous Chat is still in progress.
func TestOngoingActionSummaryUsesPlanMeaning(t *testing.T) {
	record := model.Action{Type: model.ActionTypeChat, Background: "A asked about dinner", Reason: "I will reply",
		PlanJSON: `{"guidance":"Tell A I can meet tomorrow","session_id":42}`}
	summary := formatOngoingAction(record)
	for _, part := range []string{"Tell A I can meet tomorrow", "(session_id=42)"} {
		if !strings.Contains(summary, part) {
			t.Fatalf("missing %q in %q", part, summary)
		}
	}
	if strings.Contains(summary, "guidance\"") || strings.Contains(summary, "\"session_id\"") {
		t.Fatalf("storage JSON entered the Situation: %q", summary)
	}
}
