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
			{Function: rawFunction{Name: tools.ToolNameReadKBEvidence.String(), Arguments: `{"chunk_ids":[4,19,20]}`}},
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

	if len(events) != 5 {
		t.Fatalf("expected five activity events, got %d", len(events))
	}
	wantIDs := []string{"101:thinking", "101:tool:0", "101:tool:1", "101:tool:2", "102:guidance"}
	for index, wantID := range wantIDs {
		if events[index].ID != wantID {
			t.Errorf("event %d ID = %q, want %q", index, events[index].ID, wantID)
		}
	}
	if events[1].Target != "migration design" || events[2].Target != "3" || events[3].Target != "42" {
		t.Errorf("KB targets = %q, %q, %q; want query, evidence count, and KB ID", events[1].Target, events[2].Target, events[3].Target)
	}
	if events[0].PersonID != 11 || events[4].PersonID != 12 {
		t.Errorf("event ownership = %d, %d; want 11, 12", events[0].PersonID, events[4].PersonID)
	}
}

func TestActivityTextToolsExposeOnlyDisplayParameters(t *testing.T) {
	cases := []struct {
		tool      string
		arguments string
		key       string
		want      interface{}
	}{
		{tools.ToolNameSearchTextFile.String(), `{"file_path":"output/script.js","query":"render","max_matches":10}`, "query", "render"},
		{tools.ToolNameReadTextLines.String(), `{"file_path":"output/script.js","start_line":14,"max_lines":30}`, "start_line", 14},
		{tools.ToolNameReadTextBlock.String(), `{"file_path":"output/script.js","start_byte_offset":4096,"max_bytes":2048}`, "start_byte_offset", int64(4096)},
	}
	for _, tc := range cases {
		params := extractTextToolParams(1, tc.tool, tc.arguments)
		if got := params[tc.key]; got != tc.want {
			t.Errorf("%s %s = %v, want %v", tc.tool, tc.key, got, tc.want)
		}
		if _, exposed := params["file_path"]; exposed {
			t.Errorf("%s duplicated file_path in tool params", tc.tool)
		}
	}
	if params := extractTextToolParams(1, tools.ToolNameWriteTextFile.String(), `{"content":"private"}`); params != nil {
		t.Errorf("write tool arguments leaked: %v", params)
	}
}
