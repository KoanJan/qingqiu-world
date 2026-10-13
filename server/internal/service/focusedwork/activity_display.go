package focusedwork

import (
	"encoding/json"
	"fmt"
	"strings"

	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/focusedwork/tools"
)

// activityTargetKeys maps every registered FocusedWork tool to the argument
// fields that best identify its user-visible target. Keys are raw names so an
// unknown historical tool never falls back to bash's argument contract.
var activityTargetKeys = map[string][]string{
	tools.ToolNameBash.String():                {"command"},
	tools.ToolNameReadTextFile.String():        {"file_path"},
	tools.ToolNameWriteTextFile.String():       {"file_path"},
	tools.ToolNameEditTextFile.String():        {"file_path"},
	tools.ToolNameWriteNotes.String():          {"content", "entry_type"},
	tools.ToolNameWebSearch.String():           {"query"},
	tools.ToolNameSendJinshu.String():          {"receiver", "topic"},
	tools.ToolNameScanMyExperience.String():    {"keyword"},
	tools.ToolNameRecallMyExperience.String():  {"exp_id"},
	tools.ToolNameSearchChatHistories.String(): {"query"},
	tools.ToolNameScanJinshu.String():          {"page"},
	tools.ToolNameReadJinshu.String():          {"jinshu_id"},
	tools.ToolNameCopyFromJinshu.String():      {"target_relative_dir", "jinshu_id"},
	tools.ToolNameScanKB.String():              {"query"},
	tools.ToolNameListKBDocuments.String():     {"kb_id"},
	tools.ToolNameReadKBEvidence.String():      {"chunk_ids"},
	tools.ToolNameUseWorkspace.String():        {"workspace_id"},
	tools.ToolNameReadTextLines.String():       {"file_path"},
	tools.ToolNameReadTextBlock.String():       {"file_path"},
	tools.ToolNameSearchTextFile.String():      {"file_path", "query"},
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
// workPersonIDs is the authoritative Work-to-agent attribution map; events
// whose historical Work cannot be mapped are logged and omitted.
func BuildActivityEvents(interactions []model.Interaction, workPersonIDs map[int64]int64) []schema.ActivityEvent {
	events := make([]schema.ActivityEvent, 0)

	for _, interaction := range interactions {
		personID, ok := workPersonIDs[interaction.WorkID]
		if !ok || personID <= 0 {
			applogger.Error("activity: interaction has no valid work owner",
				"interaction_id", interaction.ID, "work_id", interaction.WorkID)
			continue
		}
		switch interaction.Type {
		case model.InteractionTypeResponse:
			events = append(events, parseResponseInteraction(&interaction, personID)...)
		case model.InteractionTypeGuidance:
			events = append(events, schema.ActivityEvent{
				ID:       fmt.Sprintf("%d:guidance", interaction.ID),
				Time:     interaction.CreatedAt.Format("2006-01-02 15:04:05"),
				Type:     "guidance",
				Content:  extractGuidance(&interaction),
				PersonID: personID,
			})
		default:
			applogger.Error("activity: unsupported interaction type reached display builder",
				"interaction_id", interaction.ID, "work_id", interaction.WorkID, "type", interaction.Type)
		}
	}

	return events
}

// parseResponseInteraction parses a type=2 interaction and returns the derived events.
func parseResponseInteraction(interaction *model.Interaction, personID int64) []schema.ActivityEvent {
	var data interactionDataResponse
	if err := json.Unmarshal([]byte(interaction.Data), &data); err != nil {
		applogger.Error("activity: failed to parse response interaction data",
			"interaction_id", interaction.ID, "work_id", interaction.WorkID, "error", err)
		return nil
	}

	timeStr := interaction.CreatedAt.Format("2006-01-02 15:04:05")
	var events []schema.ActivityEvent

	// Thinking event
	if data.Content != "" {
		events = append(events, schema.ActivityEvent{
			ID:       fmt.Sprintf("%d:thinking", interaction.ID),
			Time:     timeStr,
			Type:     "thinking",
			Content:  strings.TrimSpace(data.Content),
			PersonID: personID,
		})
	}

	// Tool call events
	for toolCallIndex, tc := range data.ToolCalls {
		events = append(events, buildToolCallEvent(timeStr, interaction.ID, toolCallIndex, personID, &tc))
	}

	return events
}

// buildToolCallEvent converts a raw tool call into an ActivityEvent with tool name and target.
func buildToolCallEvent(timeStr string, interactionID int64, toolCallIndex int, personID int64, tc *rawToolCall) schema.ActivityEvent {
	keys := activityTargetKeys[tc.Function.Name]
	target := ""
	if tc.Function.Name == tools.ToolNameReadKBEvidence.String() {
		target = extractReadKBEvidenceTarget(interactionID, tc.Function.Arguments)
	} else if len(keys) > 0 {
		target = extractTarget(interactionID, tc.Function.Name, tc.Function.Arguments, keys)
	}

	return schema.ActivityEvent{
		ID:         fmt.Sprintf("%d:tool:%d", interactionID, toolCallIndex),
		Time:       timeStr,
		Type:       "tool_call",
		Tool:       tc.Function.Name,
		Target:     target,
		ToolParams: extractTextToolParams(interactionID, tc.Function.Name, tc.Function.Arguments),
		PersonID:   personID,
	}
}

// extractTextToolParams exposes only the file-operation arguments needed for
// Activity wording; other tool arguments may contain private or long content.
func extractTextToolParams(interactionID int64, toolName, argumentsJSON string) map[string]interface{} {
	switch toolName {
	case tools.ToolNameReadTextLines.String(), tools.ToolNameReadTextBlock.String(), tools.ToolNameSearchTextFile.String():
	default:
		return nil
	}
	var args struct {
		Query           string `json:"query"`
		StartLine       *int   `json:"start_line"`
		MaxLines        *int   `json:"max_lines"`
		StartByteOffset *int64 `json:"start_byte_offset"`
		MaxBytes        *int   `json:"max_bytes"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		applogger.Error("activity: failed to parse text-tool arguments", "interaction_id", interactionID, "tool", toolName, "error", err)
		return nil
	}
	switch toolName {
	case tools.ToolNameSearchTextFile.String():
		return map[string]interface{}{"query": args.Query}
	case tools.ToolNameReadTextLines.String():
		startLine, maxLines := 1, 200
		if args.StartLine != nil {
			startLine = *args.StartLine
		}
		if args.MaxLines != nil {
			maxLines = *args.MaxLines
		}
		return map[string]interface{}{"start_line": startLine, "max_lines": maxLines}
	case tools.ToolNameReadTextBlock.String():
		startByteOffset, maxBytes := int64(0), 8192
		if args.StartByteOffset != nil {
			startByteOffset = *args.StartByteOffset
		}
		if args.MaxBytes != nil {
			maxBytes = *args.MaxBytes
		}
		return map[string]interface{}{"start_byte_offset": startByteOffset, "max_bytes": maxBytes}
	}
	return nil
}

// extractTarget extracts the first non-empty value from the arguments JSON.
func extractTarget(interactionID int64, toolName, argumentsJSON string, keys []string) string {
	if argumentsJSON == "" {
		return ""
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		applogger.Error("activity: failed to parse tool-call arguments",
			"interaction_id", interactionID, "tool", toolName, "error", err)
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

// extractReadKBEvidenceTarget returns a display-safe evidence count instead of
// exposing internal chunk IDs in the user-facing Activity timeline.
func extractReadKBEvidenceTarget(interactionID int64, argumentsJSON string) string {
	if argumentsJSON == "" {
		return ""
	}

	var args struct {
		ChunkIDs []int64 `json:"chunk_ids"`
	}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		applogger.Error("activity: failed to parse read_kb_evidence arguments",
			"interaction_id", interactionID, "error", err)
		return ""
	}
	if len(args.ChunkIDs) == 0 {
		return ""
	}
	return fmt.Sprintf("%d", len(args.ChunkIDs))
}

// extractGuidance extracts the guidance text from a type=3 interaction's Data JSON.
func extractGuidance(interaction *model.Interaction) string {
	var data struct {
		Guidance string `json:"guidance"`
	}
	if err := json.Unmarshal([]byte(interaction.Data), &data); err != nil {
		applogger.Error("activity: failed to parse guidance interaction data",
			"interaction_id", interaction.ID, "work_id", interaction.WorkID, "error", err)
		return ""
	}
	return data.Guidance
}
