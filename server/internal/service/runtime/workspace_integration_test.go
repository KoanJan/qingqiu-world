package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/aos"
	"qingqiu-world-server/internal/service/eventqueue"
	focusedtools "qingqiu-world-server/internal/service/focusedwork/tools"
	"qingqiu-world-server/internal/service/memory"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestWorkChoosesWorkspaceWithoutSessionAndReusesItAcrossSessions verifies
// file ownership and the declared relationship independently of conversation.
func TestWorkChoosesWorkspaceWithoutSessionAndReusesItAcrossSessions(t *testing.T) {
	root := t.TempDir()
	settings := config.Get()
	oldAOS, oldMeta := settings.AOSRoot, settings.AOSMetaRoot
	settings.AOSRoot, settings.AOSMetaRoot = filepath.Join(root, "aos"), filepath.Join(root, "meta")
	t.Cleanup(func() { settings.AOSRoot, settings.AOSMetaRoot = oldAOS, oldMeta })
	previous := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(root, "runtime.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	if err := db.AutoMigrate(&model.Person{}, &model.Event{}, &model.Decision{}, &model.Action{}, &model.ActionEffect{}, &model.Work{}, &model.Workspace{}, &model.WorkspaceUse{}, &model.MemoryTerm{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Person{ID: 21, Name: "owner", Type: model.PersonTypeAI}).Error; err != nil {
		t.Fatal(err)
	}
	// A migrated Session directory can have the same number as the next
	// Workspace row. Its files and paired metadata must remain untouched.
	legacyDir := filepath.Join(aos.GetAgentOwnedSpacePath(21), "work", "2")
	legacyMeta := filepath.Join(aos.GetAgentMetaPath(21), "work", "2", ".meta")
	for _, dir := range []string{legacyDir, legacyMeta} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	legacyFile := filepath.Join(legacyDir, "existing.txt")
	if err := os.WriteFile(legacyFile, []byte("historical"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Workspace{PersonID: 21, RelativePath: "work/2", Name: "Earlier work"}).Error; err != nil {
		t.Fatal(err)
	}
	r := &agentRuntime{agentPersonID: 21, agentConfigID: 41}
	makeAction := func(plan *action.WorkPlan) action.Action {
		decision := model.Decision{PersonID: 21}
		if err := db.Create(&decision).Error; err != nil {
			t.Fatal(err)
		}
		record := model.Action{DecisionID: decision.ID, Type: model.ActionTypeStartFocusedWork, Status: model.ActionStatusInProgress, PlanJSON: "{}"}
		if err := db.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
		return action.Action{ID: record.ID, Type: action.StartFocusedWork, WorkPlan: plan}
	}
	first, ok := r.newWork(&Situation{Matter: SituationMatter{Description: "Read a Jinshu attachment"}}, makeAction(&action.WorkPlan{Guidance: "Read the attachment", NewWorkspace: &action.NewWorkspacePlan{Name: "Documents", Purpose: "Review the received material"}}))
	if !ok || first.sessionID != 0 || first.workspaceID <= 0 {
		t.Fatalf("sessionless Work was not created: %+v ok=%v", first, ok)
	}
	if first.workspaceID != 3 {
		t.Fatalf("new Workspace ID %d collided with historical work/2", first.workspaceID)
	}
	if content, err := os.ReadFile(legacyFile); err != nil || string(content) != "historical" {
		t.Fatalf("historical directory was altered: %q %v", content, err)
	}
	record, err := dops.GetOwnedWorkspace(21, first.workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if record.RelativePath != "work/3" {
		t.Fatalf("new Workspace changed the work directory hierarchy: %s", record.RelativePath)
	}
	dir, err := aos.ResolveRegisteredPath(*record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "output")); !os.IsNotExist(err) {
		t.Fatalf("new Workspace should not create an output directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "evidence.txt"), []byte("kept"), 0644); err != nil {
		t.Fatal(err)
	}
	metaDir, err := aos.GetWorkspaceMetaDir(*record)
	if err != nil {
		t.Fatal(err)
	}
	if metaDir != filepath.Join(aos.GetAgentMetaPath(21), record.RelativePath, ".meta") {
		t.Fatalf("Workspace metadata is not paired with its resource directory: %s", metaDir)
	}
	firstNotes := focusedtools.NewWriteNotesTool(21, 4000, metaDir)
	if _, err := firstNotes.Execute(map[string]interface{}{"entry_type": "finding", "content": "The attachment changed evidence.txt"}); err != nil {
		t.Fatal(err)
	}
	second, ok := r.newWork(&Situation{Matter: SituationMatter{Event: &eventqueue.AgentEvent{SessionID: 74}}}, makeAction(&action.WorkPlan{Guidance: "Continue using the documents", WorkspaceID: first.workspaceID}))
	if !ok || second.workspaceID != first.workspaceID || second.sessionID != 74 {
		t.Fatalf("Workspace reuse failed: %+v ok=%v", second, ok)
	}
	secondNotes := focusedtools.NewWriteNotesTool(21, 4000, metaDir)
	if !strings.Contains(secondNotes.ReadNotes(), "The attachment changed evidence.txt") {
		t.Fatal("the later Work did not inherit the Workspace's notes")
	}
	var uses []model.WorkspaceUse
	if err := db.Where("workspace_id = ?", first.workspaceID).Find(&uses).Error; err != nil {
		t.Fatal(err)
	}
	if len(uses) != 2 || uses[0].SourceID == uses[1].SourceID {
		t.Fatalf("declared uses = %+v", uses)
	}
	third, ok := r.newWork(&Situation{Matter: SituationMatter{Description: "Start another task"}}, makeAction(&action.WorkPlan{Guidance: "Start another task", NewWorkspace: &action.NewWorkspacePlan{Name: "Other"}}))
	if !ok || third.workspaceID != 4 {
		t.Fatalf("the next Workspace did not continue after the occupied directory: %+v ok=%v", third, ok)
	}
	if content, err := os.ReadFile(filepath.Join(dir, "evidence.txt")); err != nil || string(content) != "kept" {
		t.Fatalf("Workspace file changed: %q %v", content, err)
	}
	browse, err := executeWorkspaceRecall(21, recallArguments{Query: "Documents"})
	if err != nil || !strings.Contains(browse, "Review the received material") {
		t.Fatalf("Workspace browse = %s err=%v", browse, err)
	}
	detail, err := executeWorkspaceRecall(21, recallArguments{WorkspaceID: first.workspaceID})
	if err != nil || !strings.Contains(detail, "Continue using the documents") {
		t.Fatalf("Workspace use history = %s err=%v", detail, err)
	}
	workPage := memory.RecallPage{Items: []memory.RecallItem{{SourceKind: model.MemorySourceWork, SourceID: second.ID}}}
	linked, err := encodeWorkRecallWithWorkspace(21, workPage)
	if err != nil || !strings.Contains(linked, "Documents") {
		t.Fatalf("Work default Workspace = %s err=%v", linked, err)
	}
}
