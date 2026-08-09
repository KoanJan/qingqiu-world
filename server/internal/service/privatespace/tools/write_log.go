package tools

import (
	"fmt"

	"qingqiu-world-server/internal/service/llm"
)

// WriteLogTool appends records to the agent's private activity log.
// The actual log writing is delegated to the appendLog function
// to avoid circular imports between privatespace and privatespace/tools.
type WriteLogTool struct {
	appendLog func(personID int64, content string) error
	personID  int64
}

// NewWriteLogTool creates a WriteLogTool.
// appendLog is typically privatespace.AppendLog.
func NewWriteLogTool(personID int64, appendLog func(int64, string) error) *WriteLogTool {
	return &WriteLogTool{
		personID:  personID,
		appendLog: appendLog,
	}
}

func (t *WriteLogTool) Name() string { return "write_log" }
func (t *WriteLogTool) Description() string {
	return "Append a record to your private activity log"
}

func (t *WriteLogTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name: "write_log",
		Description: "Append a record to your private activity log. A timestamp is automatically recorded — do NOT write a date header. " +
			"Use this to log: (1) why you entered your private space, (2) what you found or noticed here, (3) what you did, and (4) what you left behind. " +
			"Keep it concise — one to three sentences. This is an activity log, not a diary.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"content": map[string]interface{}{
					"type":        "string",
					"description": "A brief log record covering: why entered, what found, what did, what left. Timestamp is auto-added. Keep under 3 sentences.",
				},
			},
			"required": []string{"content"},
		},
	}
}

func (t *WriteLogTool) Execute(args map[string]interface{}) (string, error) {
	content, _ := args["content"].(string)
	if content == "" {
		return "", fmt.Errorf("content is required")
	}

	if err := t.appendLog(t.personID, content); err != nil {
		return "", fmt.Errorf("append log: %w", err)
	}

	return "Log record saved.", nil
}
