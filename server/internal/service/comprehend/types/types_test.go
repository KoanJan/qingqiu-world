package types_test

import (
	"encoding/json"
	"strings"
	"testing"

	"qingqiu-world-server/internal/service/comprehend/types"
	"qingqiu-world-server/internal/service/llm"
)

// TestPersonPurposeSchemaAndPrompt checks the LLM contract and the absence of
// an invented purpose claim when the inferred category is other.
func TestPersonPurposeSchemaAndPrompt(t *testing.T) {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	rawSchema := llm.GenerateSchema[types.PersonState]()
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		t.Fatalf("%v: %s", err, rawSchema)
	}
	var purpose struct {
		Type string `json:"type"`
		Enum []int  `json:"enum"`
	}
	if err := json.Unmarshal(schema.Properties["purpose"], &purpose); err != nil {
		t.Fatal(err)
	}
	if purpose.Type != "integer" || len(purpose.Enum) != 4 {
		t.Fatalf("purpose schema = %+v, want four integer choices", purpose)
	}
	for i, value := range purpose.Enum {
		if value != i {
			t.Fatalf("purpose enum[%d] = %d, want %d", i, value, i)
		}
	}

	state := types.PersonState{Emotion: "calm", Purpose: types.PersonPurposeOther, Situation: "unknown"}
	text := state.ToNaturalLanguage("小青")
	if strings.Contains(text, "other") || strings.Contains(text, "request") || strings.Contains(text, "is likely") {
		t.Fatalf("other purpose added a claim to Chat: %q", text)
	}
	state.Purpose = types.PersonPurposeRequest
	if !strings.Contains(state.ToNaturalLanguage("小青"), "requesting information or action") {
		t.Fatal("explicit request purpose was omitted")
	}
}
