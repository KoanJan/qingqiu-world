package runtime

import (
	"encoding/json"
	"testing"

	"qingqiu-world-server/internal/service/llm"
)

// TestDecisionSchemaExcludesRuntimeIdentity keeps persistence fields out of
// the LLM contract even though the same structs cross the execution boundary.
func TestDecisionSchemaExcludesRuntimeIdentity(t *testing.T) {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(llm.GenerateSchema[DecisionResult](), &schema); err != nil {
		t.Fatal(err)
	}
	if _, present := schema.Properties["accepted"]; present {
		t.Fatal("Accepted leaked into the LLM schema")
	}
	var actions struct {
		Items struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"items"`
	}
	if err := json.Unmarshal(schema.Properties["actions"], &actions); err != nil {
		t.Fatal(err)
	}
	if _, present := actions.Items.Properties["id"]; present {
		t.Fatal("persisted Action ID leaked into the LLM schema")
	}
}
