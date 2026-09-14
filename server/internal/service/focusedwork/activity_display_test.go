package focusedwork

import (
	"encoding/json"
	"testing"
	"time"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/focusedwork/tools"
)

// TestActivityTargetKeysCoverRegisteredTools keeps newly registered tools from
// silently losing their visible target in the Activity timeline.
func TestActivityTargetKeysCoverRegisteredTools(t *testing.T) {
	for _, toolName := range tools.AllToolNames() {
		keys, ok := activityTargetKeys[toolName.String()]
		if !ok || len(keys) == 0 {
			t.Errorf("registered tool %q has no Activity target strategy", toolName.String())
		}
	}
}

// TestBuildActivityEventsPreservesStableIDsAndWorkOwnership verifies that a
// page can prepend older events without changing React keys or agent labels.
func TestBuildActivityEventsPreservesStableIDsAndWorkOwnership(t *testing.T) {
	responseData, err := json.Marshal(interactionDataResponse{
		Content: "I will inspect the knowledge base.",
		ToolCalls: []rawToolCall{
			{Function: rawFunction{Name: tools.ToolNameScanKB.String(), Arguments: `{"query":"migration design"}`}},
			{Function: rawFunction{Name: tools.ToolNameListKBDocuments.String(), Arguments: `{"kb_id":42}`}},
		},
	})
	if err != nil {
		t.Fatalf("marshal response data: %v", err)
	}

	createdAt := time.Date(2026, time.September, 15, 10, 30, 0, 0, time.UTC)
	events := BuildActivityEvents([]model.Interaction{
		{
			ID:        101,
			WorkID:    7,
			Type:      model.InteractionTypeResponse,
			Data:      string(responseData),
			CreatedAt: createdAt,
		},
		{
			ID:        102,
			WorkID:    8,
			Type:      model.InteractionTypeGuidance,
			Data:      `{"guidance":"Continue with the current focus."}`,
			CreatedAt: createdAt,
		},
	}, map[int64]int64{7: 11, 8: 12})

	if len(events) != 4 {
		t.Fatalf("expected four activity events, got %d", len(events))
	}
	wantIDs := []string{"101:thinking", "101:tool:0", "101:tool:1", "102:guidance"}
	for index, wantID := range wantIDs {
		if events[index].ID != wantID {
			t.Errorf("event %d ID = %q, want %q", index, events[index].ID, wantID)
		}
	}
	if events[1].Target != "migration design" || events[2].Target != "42" {
		t.Errorf("KB targets = %q, %q; want query and KB ID", events[1].Target, events[2].Target)
	}
	if events[0].PersonID != 11 || events[3].PersonID != 12 {
		t.Errorf("event ownership = %d, %d; want 11, 12", events[0].PersonID, events[3].PersonID)
	}
}
