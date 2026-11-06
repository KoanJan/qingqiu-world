package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// ProbeCapability reports whether a concrete production client call succeeded.
type ProbeCapability struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type ProbeResult struct {
	RequiredTools    ProbeCapability `json:"required_tools"`
	StructuredOutput ProbeCapability `json:"structured_output"`
	CheckedAt        time.Time       `json:"checked_at"`
}

var probeMessages = []Message{
	{Role: "user", Content: "This is a compatibility check. Call compatibility_check with value ok. Do not perform any action."},
}

var probeParameters = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"value": map[string]interface{}{"type": "string", "enum": []string{"ok"}},
	},
	"required":             []string{"value"},
	"additionalProperties": false,
}

// Probe uses the same client methods as runtime requests and never executes a tool.
// A single deadline bounds both requests; each capability is reported separately.
func Probe(ctx context.Context, baseURL, apiKey, modelID string) ProbeResult {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cm := NewChatModel(baseURL, apiKey, modelID)
	result := ProbeResult{}
	tool := FunctionDefinition{Name: "compatibility_check", Description: "Report protocol compatibility", Parameters: probeParameters}

	response, err := cm.ChatWithRequiredTools(ctx, probeMessages, []FunctionDefinition{tool})
	if err == nil {
		if response.FinishReason != "tool_calls" || len(response.ToolCalls) != 1 || response.ToolCalls[0].Function.Name != tool.Name {
			err = fmt.Errorf("expected one compatibility_check tool call")
		} else {
			err = checkProbeArguments(response.ToolCalls[0].Function.Arguments)
		}
	}
	result.RequiredTools = probeCapability(err, apiKey)

	schema := JSONSchemaDefinition{Name: tool.Name, Schema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string","enum":["ok"]}},"required":["value"],"additionalProperties":false}`)}
	arguments, err := cm.tryFunctionCallJSON(ctx, probeMessages, schema)
	if err == nil {
		err = checkProbeArguments(arguments)
	}
	result.StructuredOutput = probeCapability(err, apiKey)
	result.CheckedAt = time.Now().UTC()
	return result
}

func checkProbeArguments(arguments string) error {
	var value struct {
		Value string `json:"value"`
	}
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || value.Value != "ok" {
		return fmt.Errorf("invalid compatibility_check arguments")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("invalid compatibility_check arguments")
	}
	return nil
}

func probeCapability(err error, apiKey string) ProbeCapability {
	if err == nil {
		return ProbeCapability{OK: true}
	}
	message := err.Error()
	if apiKey != "" {
		message = strings.ReplaceAll(message, apiKey, "[redacted]")
	}
	if len(message) > 500 {
		message = message[:500]
	}
	return ProbeCapability{Error: message}
}
