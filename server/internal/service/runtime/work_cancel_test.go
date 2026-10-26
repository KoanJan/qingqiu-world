package runtime

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/action"
)

// TestCancelWorkInterruptsFocus checks the state transition and the running
// context together; changing the database row alone must not leave Focus alive.
func TestCancelWorkInterruptsFocus(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/cancel-work.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Work{}, &model.Action{}); err != nil {
		t.Fatal(err)
	}
	oldDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = oldDB })

	createWork := func(status model.WorkStatus) *work {
		t.Helper()
		row := model.Work{PersonID: 1, SessionID: 2, Description: "Contact Zero", Status: status}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		return &work{ID: row.ID}
	}
	checkCancelled := func(w *work) {
		t.Helper()
		var row model.Work
		if err := db.First(&row, w.ID).Error; err != nil {
			t.Fatal(err)
		}
		if row.Status != model.WorkStatusAbandoned || row.FocusPhase != model.FocusPhaseCancelled {
			t.Fatalf("work remains executable after cancellation: %+v", row)
		}
		if w.cancelActionID != 815 || w.cancelReason != "Patrick asked me to stop" {
			t.Fatalf("cancel provenance missing: id=%d reason=%q", w.cancelActionID, w.cancelReason)
		}
	}
	act := action.Action{ID: 815, Reason: "Patrick asked me to stop"}

	running := createWork(model.WorkStatusRunning)
	ctx, release := running.executionContext(context.Background())
	defer release()
	if !running.requestCancel(act) {
		t.Fatal("running work was not cancelled")
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("Focus context remained active: %v", ctx.Err())
	}
	checkCancelled(running)
	if running.requestCancel(action.Action{ID: 816, Reason: "duplicate"}) {
		t.Fatal("duplicate cancellation changed an already stopped work")
	}
	checkCancelled(running)

	beforeStart := createWork(model.WorkStatusRunning)
	if !beforeStart.requestCancel(act) {
		t.Fatal("work was not cancelled before its goroutine started")
	}
	lateCtx, lateRelease := beforeStart.executionContext(context.Background())
	defer lateRelease()
	if lateCtx.Err() != context.Canceled {
		t.Fatalf("late Focus context remained active: %v", lateCtx.Err())
	}
	checkCancelled(beforeStart)

	completed := createWork(model.WorkStatusCompleted)
	if completed.requestCancel(act) {
		t.Fatal("completed work accepted cancellation")
	}
	var completedRow model.Work
	if err := db.First(&completedRow, completed.ID).Error; err != nil {
		t.Fatal(err)
	}
	if completedRow.Status != model.WorkStatusCompleted {
		t.Fatalf("completed work status changed: %v", completedRow.Status)
	}

	viaRuntime := createWork(model.WorkStatusRunning)
	storedAction := model.Action{DecisionID: 1, Type: model.ActionTypeCancelFocusedWork, Status: model.ActionStatusInProgress}
	if err := db.Create(&storedAction).Error; err != nil {
		t.Fatal(err)
	}
	runtime := &agentRuntime{activeWorks: []*work{viaRuntime}}
	runtime.executeActions(context.Background(), nil, []action.Action{{
		ID: storedAction.ID, Type: action.CancelFocusedWork, Reason: "Patrick asked me to stop",
		WorkGuidance: &action.WorkGuidance{TargetWorkID: viaRuntime.ID, Guidance: "I should stop"},
	}})
	if len(runtime.activeWorks) != 0 {
		t.Fatalf("cancelled work remains in the active roster: %+v", runtime.activeWorks)
	}
	var endedAction model.Action
	if err := db.First(&endedAction, storedAction.ID).Error; err != nil {
		t.Fatal(err)
	}
	if endedAction.Status != model.ActionStatusEnded {
		t.Fatalf("cancel action remained active: %v", endedAction.Status)
	}
}
