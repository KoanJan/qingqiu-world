package dops

import (
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// TestListActivityInteractions verifies that Activity pagination never reads
// request prompts and keeps every cursor page chronological for the UI.
func TestListActivityInteractions(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("DATA_ROOT", tempRoot)
	t.Setenv("LOG_DIR", filepath.Join(tempRoot, "logs"))
	applogger.Init()
	database.Init()

	work := model.Work{PersonID: 1, SessionID: 1, Description: "Activity pagination"}
	if err := database.DB.Create(&work).Error; err != nil {
		t.Fatalf("create work: %v", err)
	}
	interactions := []model.Interaction{
		{SessionID: 1, WorkID: work.ID, Iteration: 1, Type: model.InteractionTypeRequest, Data: "large prompt that must not reach Activity"},
		{SessionID: 1, WorkID: work.ID, Iteration: 1, Type: model.InteractionTypeResponse, Data: `{"content":"first"}`},
		{SessionID: 1, WorkID: work.ID, Iteration: 2, Type: model.InteractionTypeGuidance, Data: `{"guidance":"second"}`},
		{SessionID: 1, WorkID: work.ID, Iteration: 2, Type: model.InteractionTypeResponse, Data: `{"content":"third"}`},
	}
	for _, interaction := range interactions {
		if err := database.DB.Create(&interaction).Error; err != nil {
			t.Fatalf("create interaction: %v", err)
		}
	}

	firstPage, hasMore, err := ListSessionActivityInteractions(1, 0, 2)
	if err != nil {
		t.Fatalf("read first activity page: %v", err)
	}
	if !hasMore || len(firstPage) != 2 {
		t.Fatalf("first page = %d entries, hasMore=%t; want two entries with more", len(firstPage), hasMore)
	}
	if firstPage[0].Type != model.InteractionTypeGuidance || firstPage[1].Type != model.InteractionTypeResponse {
		t.Errorf("first page types = %d, %d; want guidance then response in chronological order", firstPage[0].Type, firstPage[1].Type)
	}

	secondPage, hasMore, err := ListSessionActivityInteractions(1, firstPage[0].ID, 2)
	if err != nil {
		t.Fatalf("read second activity page: %v", err)
	}
	if hasMore || len(secondPage) != 1 {
		t.Fatalf("second page = %d entries, hasMore=%t; want one final entry", len(secondPage), hasMore)
	}
	if secondPage[0].Type != model.InteractionTypeResponse || secondPage[0].Data != `{"content":"first"}` {
		t.Errorf("second page unexpectedly included request data: %+v", secondPage[0])
	}
}
