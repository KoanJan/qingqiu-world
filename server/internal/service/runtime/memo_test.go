package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/action"
	"qingqiu-world-server/internal/service/memo"
)

// TestMemoActionCausality checks the real persistence boundary: each changed
// memo has an owned ActionEffect, while conflicts and no-ops do not.
func TestMemoActionCausality(t *testing.T) {
	db := setupMemoTestDB(t)
	person := model.Person{Name: "keeper", Type: model.PersonTypeAI, Status: model.PersonStatusActive}
	other := model.Person{Name: "other", Type: model.PersonTypeAI, Status: model.PersonStatusActive}
	for _, p := range []*model.Person{&person, &other} {
		if err := db.Create(p).Error; err != nil {
			t.Fatal(err)
		}
	}
	runtime := &agentRuntime{agentPersonID: person.ID}
	pin := true
	create := action.MemoPlan{Operation: model.MemoOperationCreate, Content: "I want to check the report later.", Pinned: &pin}
	createdAction := persistMemoTestAction(t, db, person.ID, create)
	runtime.handleManageMemo(createdAction)
	var records []model.Memo
	if err := db.Find(&records).Error; err != nil || len(records) != 1 {
		t.Fatalf("create memo: records=%+v err=%v", records, err)
	}
	record := records[0]
	if record.Revision != 1 || !record.Pinned || record.Status != model.MemoStatusActive {
		t.Fatalf("wrong initial memo: %+v", record)
	}
	assertMemoEffects(t, db, record.ID, 1)
	assertEndedMemoAction(t, db, createdAction.ID)

	initialRecall, err := executeMemoRecall(person.ID, fmt.Sprintf(`{"memo_id":%d}`, record.ID))
	if err != nil || !strings.Contains(initialRecall, create.Content) || !strings.Contains(initialRecall, fmt.Sprintf(`"action_id":%d`, createdAction.ID)) || !strings.Contains(initialRecall, `"trigger":"heartbeat"`) {
		t.Fatalf("memo content/source unavailable: %s %v", initialRecall, err)
	}
	if _, err := executeMemoRecall(other.ID, fmt.Sprintf(`{"memo_id":%d}`, record.ID)); err == nil {
		t.Fatal("another person read the private memo")
	}
	ownerList, err := executeMemoRecall(person.ID, `{}`)
	if err != nil || !strings.Contains(ownerList, fmt.Sprintf(`"memo_id":%d`, record.ID)) ||
		!strings.Contains(ownerList, `"created_at":"`+record.CreatedAt.Format("2006-01-02T15:04:05")) ||
		!strings.Contains(ownerList, `"updated_at":"`+record.UpdatedAt.Format("2006-01-02T15:04:05")) {
		t.Fatalf("owner cannot browse memo: %s %v", ownerList, err)
	}
	foreignList, err := executeMemoRecall(other.ID, `{}`)
	if err != nil || strings.Contains(foreignList, fmt.Sprintf(`"memo_id":%d`, record.ID)) {
		t.Fatalf("memo leaked into another owner's list: %s %v", foreignList, err)
	}
	pinned, err := memo.PinnedOverview(person.ID, 3)
	if err != nil || len(pinned) != 1 || pinned[0].ID != record.ID {
		t.Fatalf("pinned reminder unavailable: %+v %v", pinned, err)
	}

	revise := action.MemoPlan{Operation: model.MemoOperationRevise, MemoID: record.ID, Revision: 1, Content: "I checked it, and want to compare the figures."}
	revisedAction := persistMemoTestAction(t, db, person.ID, revise)
	runtime.handleManageMemo(revisedAction)
	assertMemoEffects(t, db, record.ID, 2)
	if err := db.First(&record, record.ID).Error; err != nil || record.Revision != 2 || record.Content != revise.Content {
		t.Fatalf("revision did not commit: %+v %v", record, err)
	}

	staleAction := persistMemoTestAction(t, db, person.ID, revise)
	runtime.handleManageMemo(staleAction)
	assertMemoEffects(t, db, record.ID, 2)
	assertEndedMemoAction(t, db, staleAction.ID)
	if err := db.First(&record, record.ID).Error; err != nil || record.Revision != 2 {
		t.Fatalf("stale revision changed memo: %+v %v", record, err)
	}
	foreignAction := persistMemoTestAction(t, db, other.ID, action.MemoPlan{Operation: model.MemoOperationArchive, MemoID: record.ID, Revision: 2})
	(&agentRuntime{agentPersonID: other.ID}).handleManageMemo(foreignAction)
	assertMemoEffects(t, db, record.ID, 2)

	archiveAction := persistMemoTestAction(t, db, person.ID, action.MemoPlan{Operation: model.MemoOperationArchive, MemoID: record.ID, Revision: 2})
	runtime.handleManageMemo(archiveAction)
	if err := db.First(&record, record.ID).Error; err != nil || record.Revision != 3 || record.Pinned || record.Status != model.MemoStatusArchived {
		t.Fatalf("archive did not clear pinned state: %+v %v", record, err)
	}
	assertMemoEffects(t, db, record.ID, 3)
	archivedList, err := executeMemoRecall(person.ID, `{"status":2}`)
	if err != nil || !strings.Contains(archivedList, fmt.Sprintf(`"memo_id":%d`, record.ID)) {
		t.Fatalf("archived memo cannot be browsed: %s %v", archivedList, err)
	}
	repeatAction := persistMemoTestAction(t, db, person.ID, action.MemoPlan{Operation: model.MemoOperationArchive, MemoID: record.ID, Revision: 3})
	runtime.handleManageMemo(repeatAction)
	assertMemoEffects(t, db, record.ID, 3)
	if err := db.First(&record, record.ID).Error; err != nil || record.Revision != 3 {
		t.Fatalf("no-op archive incremented revision: %+v %v", record, err)
	}
	restoreAction := persistMemoTestAction(t, db, person.ID, action.MemoPlan{Operation: model.MemoOperationRestore, MemoID: record.ID, Revision: 3})
	runtime.handleManageMemo(restoreAction)
	if err := db.First(&record, record.ID).Error; err != nil || record.Revision != 4 || record.Pinned || record.Status != model.MemoStatusActive {
		t.Fatalf("restore did not preserve unpinned state: %+v %v", record, err)
	}
	assertMemoEffects(t, db, record.ID, 4)
	setPinnedAction := persistMemoTestAction(t, db, person.ID, action.MemoPlan{Operation: model.MemoOperationSetPinned, MemoID: record.ID, Revision: 4, Pinned: &pin})
	runtime.handleManageMemo(setPinnedAction)
	if err := db.First(&record, record.ID).Error; err != nil || record.Revision != 5 || !record.Pinned {
		t.Fatalf("explicit pin did not apply: %+v %v", record, err)
	}
	unpin := false
	unpinAction := persistMemoTestAction(t, db, person.ID, action.MemoPlan{Operation: model.MemoOperationSetPinned, MemoID: record.ID, Revision: 5, Pinned: &unpin})
	runtime.handleManageMemo(unpinAction)
	if err := db.First(&record, record.ID).Error; err != nil || record.Revision != 6 || record.Pinned {
		t.Fatalf("explicit unpin did not apply: %+v %v", record, err)
	}
	assertMemoEffects(t, db, record.ID, 6)
	history, err := executeMemoRecall(person.ID, fmt.Sprintf(`{"memo_id":%d}`, record.ID))
	if err != nil || !strings.Contains(history, `"has_more_history":true`) || !strings.Contains(history, fmt.Sprintf(`"action_id":%d`, unpinAction.ID)) || !strings.Contains(history, fmt.Sprintf(`"action_id":%d`, restoreAction.ID)) {
		t.Fatalf("memo change history is incomplete: %s %v", history, err)
	}
	older, err := executeMemoRecall(person.ID, fmt.Sprintf(`{"memo_id":%d,"history_page":2}`, record.ID))
	if err != nil || !strings.Contains(older, fmt.Sprintf(`"action_id":%d`, archiveAction.ID)) || !strings.Contains(older, fmt.Sprintf(`"action_id":%d`, createdAction.ID)) {
		t.Fatalf("older memo sources cannot be reached: %s %v", older, err)
	}
}

// TestMemoChangeRollsBackWithoutCausalLink proves a failed effect insert does
// not leave a resource with an untraceable origin.
func TestMemoChangeRollsBackWithoutCausalLink(t *testing.T) {
	db := setupMemoTestDB(t)
	person := model.Person{Name: "keeper", Type: model.PersonTypeAI, Status: model.PersonStatusActive}
	if err := db.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	act := persistMemoTestAction(t, db, person.ID, action.MemoPlan{Operation: model.MemoOperationCreate, Content: "A note"})
	if err := db.Migrator().DropTable(&model.ActionEffect{}); err != nil {
		t.Fatal(err)
	}
	(&agentRuntime{agentPersonID: person.ID}).handleManageMemo(act)
	var count int64
	if err := db.Model(&model.Memo{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("memo survived a missing causal link: count=%d err=%v", count, err)
	}
	assertEndedMemoAction(t, db, act.ID)
}

// TestMemoActionIsAvailableAtHeartbeat keeps spontaneous note-taking on the
// same accepted Decide path as externally triggered memo changes.
func TestMemoActionIsAvailableAtHeartbeat(t *testing.T) {
	create := action.Action{Type: action.ManageMemo, MemoPlan: &action.MemoPlan{
		Operation: model.MemoOperationCreate, Content: "I want to revisit this idea.",
	}}
	accepted := filterValidActions([]action.Action{create}, nil, &Situation{Source: SituationSourceInternal})
	if len(accepted) != 1 || accepted[0].Type != action.ManageMemo {
		t.Fatalf("heartbeat memo action rejected: %+v", accepted)
	}
	bad := action.Action{Type: action.ManageMemo, MemoPlan: &action.MemoPlan{
		Operation: model.MemoOperationRevise, MemoID: 9, Content: "No revision supplied",
	}}
	if accepted := filterValidActions([]action.Action{bad}, nil, &Situation{Source: SituationSourceInternal}); len(accepted) != 0 {
		t.Fatalf("invalid memo plan accepted: %+v", accepted)
	}
	missingPin := action.Action{Type: action.ManageMemo, MemoPlan: &action.MemoPlan{Operation: model.MemoOperationSetPinned, MemoID: 9, Revision: 1}}
	if accepted := filterValidActions([]action.Action{missingPin}, nil, &Situation{Source: SituationSourceInternal}); len(accepted) != 0 {
		t.Fatalf("set-pinned plan without a boolean was accepted: %+v", accepted)
	}
}

func setupMemoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/memos.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Person{}, &model.Decision{}, &model.Action{}, &model.ActionEffect{}, &model.Memo{}); err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	return db
}

func persistMemoTestAction(t *testing.T, db *gorm.DB, personID int64, plan action.MemoPlan) action.Action {
	t.Helper()
	decision := model.Decision{PersonID: personID}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	record := model.Action{DecisionID: decision.ID, Type: model.ActionTypeManageMemo, PlanJSON: string(encoded), Reason: "I chose to remember this", Status: model.ActionStatusInProgress}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	return action.Action{ID: record.ID, Type: action.ManageMemo, MemoPlan: &plan}
}

func assertMemoEffects(t *testing.T, db *gorm.DB, memoID, want int64) {
	t.Helper()
	var count int64
	if err := db.Model(&model.ActionEffect{}).Where("effect_type = ? AND effect_id = ?", model.ActionEffectMemo, memoID).Count(&count).Error; err != nil || count != want {
		t.Fatalf("memo effects: got %d want %d err=%v", count, want, err)
	}
}

func assertEndedMemoAction(t *testing.T, db *gorm.DB, actionID int64) {
	t.Helper()
	var record model.Action
	if err := db.First(&record, actionID).Error; err != nil || record.Status != model.ActionStatusEnded {
		t.Fatalf("memo action did not end: %+v %v", record, err)
	}
}
