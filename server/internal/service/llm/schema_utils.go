package llm

import (
	"encoding/json"

	invopop "github.com/invopop/jsonschema"

	applogger "qingqiu-world-server/internal/logger"
)

// GenerateSchema generates a standard JSON Schema (json.RawMessage) from a Go
// struct type using invopop/jsonschema.
//
// This ensures the schema is always in sync with the struct definition,
// eliminating the risk of hand-written schemas drifting from the actual types.
// The returned json.RawMessage can be used directly in JSONSchemaDefinition.Schema.
//
// The schema is inlined (no $ref/$defs) so that all nested types are fully
// expanded and directly consumable by LLM APIs.
//
// Usage:
//
//	type MyOutput struct {
//	    Name string `json:"name" jsonschema:"description=The name of the item"`
//	}
//	schema := llm.GenerateSchema[MyOutput]()
func GenerateSchema[T any]() json.RawMessage {
	var t T
	schema := invopop.Reflect(t)

	data, err := json.Marshal(schema)
	if err != nil {
		applogger.Error("llm: failed to marshal invopop schema", "error", err)
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}

	// invopop/jsonschema generates schemas using $ref/$defs by default.
	// We inline all references to produce a flat schema where all nested types
	// are fully expanded and directly consumable by LLM APIs.
	return inlineSchemaRefs(data)
}

// inlineSchemaRefs resolves ALL $ref references in a JSON Schema by recursively
// replacing every $ref with the inlined definition from $defs, then removes $defs.
// This produces a flat schema where all nested types are fully expanded —
// required for PatchSchemaEnum and LLM API consumption.
func inlineSchemaRefs(data json.RawMessage) json.RawMessage {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return data
	}

	// Collect all definitions from $defs
	defsRaw, hasDefs := raw["$defs"]
	if !hasDefs {
		// No $defs means no references to resolve
		return data
	}

	var defs map[string]json.RawMessage
	if err := json.Unmarshal(defsRaw, &defs); err != nil {
		return data
	}

	// If top-level has $ref, inline it first to get the root schema
	if refRaw, hasRef := raw["$ref"]; hasRef {
		var ref string
		if err := json.Unmarshal(refRaw, &ref); err != nil {
			return data
		}
		defName := resolveRefName(ref)
		if defName == "" {
			return data
		}
		defRaw, ok := defs[defName]
		if !ok {
			return data
		}

		var def map[string]json.RawMessage
		if err := json.Unmarshal(defRaw, &def); err != nil {
			return data
		}

		// Build the root schema from the inlined definition
		result := make(map[string]json.RawMessage)
		if schemaRaw, ok := raw["$schema"]; ok {
			result["$schema"] = schemaRaw
		}
		for k, v := range def {
			result[k] = v
		}
		raw = result
	}

	// Remove $defs — it will no longer be needed after all refs are resolved
	delete(raw, "$defs")

	// Recursively resolve all remaining $ref references in the schema tree
	resolved := resolveAllRefs(raw, defs)

	inlined, _ := json.Marshal(resolved)
	return inlined
}

// resolveAllRefs recursively walks a JSON Schema tree and replaces every $ref
// with the inlined definition from defs. This ensures nested types (e.g., Action
// inside actions[], WorkPlan inside Action) are fully expanded.
func resolveAllRefs(node map[string]json.RawMessage, defs map[string]json.RawMessage) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(node))

	for key, val := range node {
		// Try to parse as an object — if it has $ref, inline it
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(val, &obj); err == nil {
			if refRaw, hasRef := obj["$ref"]; hasRef {
				var ref string
				if err := json.Unmarshal(refRaw, &ref); err == nil {
					defName := resolveRefName(ref)
					if defRaw, ok := defs[defName]; ok {
						// Parse the definition and recursively resolve its own refs
						var defObj map[string]json.RawMessage
						if err := json.Unmarshal(defRaw, &defObj); err == nil {
							resolved := resolveAllRefs(defObj, defs)
							resolvedJSON, _ := json.Marshal(resolved)
							result[key] = resolvedJSON
							continue
						}
					}
				}
			}
			// No $ref — recurse into children
			resolved := resolveAllRefs(obj, defs)
			resolvedJSON, _ := json.Marshal(resolved)
			result[key] = resolvedJSON
			continue
		}

		// Not an object (string, number, array, etc.) — keep as-is
		result[key] = val
	}

	return result
}

// resolveRefName extracts the definition name from a $ref path.
// E.g., "#/$defs/MyStruct" → "MyStruct"
func resolveRefName(ref string) string {
	const prefix = "#/$defs/"
	if len(ref) <= len(prefix) {
		return ""
	}
	if ref[:len(prefix)] != prefix {
		return ""
	}
	return ref[len(prefix):]
}
