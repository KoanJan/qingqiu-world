package task

import (
	"encoding/json"
	"fmt"
	"strings"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/task/tools"
)

// argumentKeys maps tool names to the parameter keys used to extract a display target.
var argumentKeys = map[tools.ToolName][]string{
	tools.ToolNameBash:               {"command"},
	tools.ToolNameWebSearch:          {"query"},
	tools.ToolNameWriteNotes:         {"content", "entry_type"},
	tools.ToolNameScanMyExperience:   {"task_description"},
	tools.ToolNameRecallMyExperience: {"query"},
	tools.ToolNameReadTextFile:       {"file_path"},
	tools.ToolNameWriteTextFile:      {"file_path"},
	tools.ToolNameEditTextFile:       {"file_path"},
}

// interactionDataResponse is the parsed form of Data JSON for type=2 interactions.
type interactionDataResponse struct {
	Content      string        `json:"content"`
	ToolCalls    []rawToolCall `json:"tool_calls"`
	FinishReason string        `json:"finish_reason"`
}

// rawToolCall mirrors the OpenAI tool call format stored in Data JSON.
type rawToolCall struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Function rawFunction `json:"function"`
}

type rawFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string, requires secondary parse
}

// BuildActivityEvents converts interactions into a flat timeline of activity events.
//
// It parses the Data JSON of each type=2 interaction to extract thinking content
// and tool_call targets. Type=3 (guidance) interactions are also included.
// Type=1 (request) is skipped.
func BuildActivityEvents(interactions []model.Interaction) []schema.ActivityEvent {
	events := make([]schema.ActivityEvent, 0)

	for _, interaction := range interactions {
		switch interaction.Type {
		case model.InteractionTypeResponse:
			events = append(events, parseResponseInteraction(&interaction)...)
		case model.InteractionTypeGuidance:
			events = append(events, schema.ActivityEvent{
				Time:    interaction.CreatedAt.Format("2006-01-02 15:04:05"),
				Type:    "guidance",
				Content: extractGuidance(interaction.Data),
			})
		}
	}

	return events
}

// parseResponseInteraction parses a type=2 interaction and returns the derived events.
func parseResponseInteraction(interaction *model.Interaction) []schema.ActivityEvent {
	var data interactionDataResponse
	if err := json.Unmarshal([]byte(interaction.Data), &data); err != nil {
		return nil
	}

	timeStr := interaction.CreatedAt.Format("2006-01-02 15:04:05")
	var events []schema.ActivityEvent

	// Thinking event
	if data.Content != "" {
		events = append(events, schema.ActivityEvent{
			Time:    timeStr,
			Type:    "thinking",
			Content: strings.TrimSpace(data.Content),
		})
	}

	// Tool call events
	for _, tc := range data.ToolCalls {
		events = append(events, buildToolCallEvent(timeStr, &tc))
	}

	return events
}

// buildToolCallEvent converts a raw tool call into an ActivityEvent with tool name and target.
func buildToolCallEvent(timeStr string, tc *rawToolCall) schema.ActivityEvent {
	keys := argumentKeys[tools.FromString(tc.Function.Name)]
	target := ""
	if len(keys) > 0 {
		target = extractTarget(tc.Function.Arguments, keys)
	}

	return schema.ActivityEvent{
		Time:   timeStr,
		Type:   "tool_call",
		Tool:   tc.Function.Name,
		Target: target,
	}
}

// extractTarget extracts the first non-empty value from the arguments JSON.
func extractTarget(argumentsJSON string, keys []string) string {
	if argumentsJSON == "" {
		return ""
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return ""
	}

	for _, key := range keys {
		if val, ok := args[key]; ok {
			s := fmt.Sprintf("%v", val)
			if s != "" {
				return s
			}
		}
	}

	return ""
}

// extractGuidance extracts the guidance text from a type=3 interaction's Data JSON.
func extractGuidance(dataJSON string) string {
	var data struct {
		Guidance string `json:"guidance"`
	}
	if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
		return ""
	}
	return data.Guidance
}
