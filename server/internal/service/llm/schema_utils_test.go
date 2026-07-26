package llm

import (
	"encoding/json"
	applogger "qingqiu-world-server/internal/logger"
	"testing"
)

// testSchemaBasic is a simple struct for testing GenerateSchema.
type testSchemaBasic struct {
	Name string `json:"name" jsonschema:"description=The name,required"`
	Age  int    `json:"age" jsonschema:"description=The age"`
}

// testSchemaWithEnum is a struct with enum constraints.
type testSchemaWithEnum struct {
	Status string `json:"status" jsonschema:"description=Current status,enum=active,enum=inactive,enum=pending,required"`
}

// testSchemaNested is a struct with nested object.
type testSchemaNested struct {
	Title string   `json:"title" jsonschema:"description=The title,required"`
	Items []string `json:"items" jsonschema:"description=List of items"`
}

// TestGenerateSchema_BasicStruct tests GenerateSchema with a basic struct.
func TestGenerateSchema_BasicStruct(t *testing.T) {
	schema := GenerateSchema[testSchemaBasic]()

	var raw map[string]interface{}
	if err := json.Unmarshal(schema, &raw); err != nil {
		applogger.Error("GenerateSchema returned invalid JSON in basic struct test", "error", err)
		t.Fatalf("GenerateSchema returned invalid JSON: %v", err)
	}

	// Must be an object type
	if typ, _ := raw["type"].(string); typ != "object" {
		t.Errorf("expected type=object, got %v", raw["type"])
	}

	// Must have properties directly at top level (inlined, no $ref)
	props, ok := raw["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("expected properties to be a map — schema may not be inlined")
	}

	// Must contain "name" and "age"
	if _, exists := props["name"]; !exists {
		t.Error("missing property 'name'")
	}
	if _, exists := props["age"]; !exists {
		t.Error("missing property 'age'")
	}

	// "name" must have description
	nameProp := props["name"].(map[string]interface{})
	if desc, _ := nameProp["description"].(string); desc != "The name" {
		t.Errorf("name description = %q, want %q", desc, "The name")
	}

	// Required must include "name"
	required, _ := raw["required"].([]interface{})
	found := false
	for _, r := range required {
		if r == "name" {
			found = true
		}
	}
	if !found {
		t.Error("'name' should be in required")
	}

	// Must NOT have $ref (schema should be inlined)
	if _, hasRef := raw["$ref"]; hasRef {
		t.Error("schema should be inlined, but found $ref")
	}
}

// TestGenerateSchema_WithEnum tests GenerateSchema with a struct that has enum constraints.
func TestGenerateSchema_WithEnum(t *testing.T) {
	schema := GenerateSchema[testSchemaWithEnum]()

	var raw map[string]interface{}
	if err := json.Unmarshal(schema, &raw); err != nil {
		applogger.Error("GenerateSchema returned invalid JSON in enum test", "error", err)
		t.Fatalf("GenerateSchema returned invalid JSON: %v", err)
	}

	props := raw["properties"].(map[string]interface{})
	statusProp := props["status"].(map[string]interface{})

	enumVals, _ := statusProp["enum"].([]interface{})
	if len(enumVals) != 3 {
		t.Fatalf("expected 3 enum values, got %d", len(enumVals))
	}

	expected := []string{"active", "inactive", "pending"}
	for i, v := range enumVals {
		if v.(string) != expected[i] {
			t.Errorf("enum[%d] = %q, want %q", i, v.(string), expected[i])
		}
	}
}

// TestGenerateSchema_NestedStruct tests GenerateSchema with a nested struct.
func TestGenerateSchema_NestedStruct(t *testing.T) {
	schema := GenerateSchema[testSchemaNested]()

	var raw map[string]interface{}
	if err := json.Unmarshal(schema, &raw); err != nil {
		applogger.Error("GenerateSchema returned invalid JSON in nested struct test", "error", err)
		t.Fatalf("GenerateSchema returned invalid JSON: %v", err)
	}

	props := raw["properties"].(map[string]interface{})

	// "items" should be an array type
	itemsProp := props["items"].(map[string]interface{})
	if typ, _ := itemsProp["type"].(string); typ != "array" {
		t.Errorf("items type = %q, want 'array'", typ)
	}
}

// TestInlineSchemaRefs_NoRef tests inlineSchemaRefs with a schema that has no $ref.
func TestInlineSchemaRefs_NoRef(t *testing.T) {
	// Schema without $ref should be returned as-is
	input := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)
	result := inlineSchemaRefs(input)

	if string(input) != string(result) {
		t.Error("schema without $ref should be returned unchanged")
	}
}

// TestResolveRefName tests resolveRefName with various ref format inputs.
func TestResolveRefName(t *testing.T) {
	tests := []struct {
		ref      string
		expected string
	}{
		{"#/$defs/MyStruct", "MyStruct"},
		{"#/$defs/testSchemaBasic", "testSchemaBasic"},
		{"#/definitions/Foo", ""}, // unsupported format
		{"", ""},                  // empty
		{"#/$defs/", ""},          // no name after prefix
	}

	for _, tt := range tests {
		got := resolveRefName(tt.ref)
		if got != tt.expected {
			t.Errorf("resolveRefName(%q) = %q, want %q", tt.ref, got, tt.expected)
		}
	}
}
