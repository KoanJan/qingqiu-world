package tools

import (
	"encoding/json"
	"fmt"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/jinshu"
	"qingqiu-world-server/internal/service/llm"
)

// ReadJinshuTool reads a single received jinshu (锦书): its metadata and the
// flat list of files it delivered. It only exposes jinshu addressed to this
// agent.
type ReadJinshuTool struct {
	personID      int64
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewReadJinshuTool creates a ReadJinshuTool for the given person.
func NewReadJinshuTool(personID int64) *ReadJinshuTool {
	return &ReadJinshuTool{personID: personID}
}

// Name returns the tool name.
func (r *ReadJinshuTool) Name() ToolName { return ToolNameReadJinshu }

// Description returns a brief description of the tool.
func (r *ReadJinshuTool) Description() string {
	return "Read a received jinshu (锦书): metadata and delivered files"
}

// Schema returns the LLM function definition for the tool.
func (r *ReadJinshuTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name: r.Name().String(),
		Description: "Read one jinshu (锦书) you have received by its id. " +
			"Returns the sender, receiver, topic, description, read status, created time, " +
			"and the list of delivered files. Use copy_from_jinshu to copy those files " +
			"into your output directory.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"jinshu_id": map[string]interface{}{
					"type":        "integer",
					"description": "The id of the received jinshu (from scan_jinshu).",
				},
			},
			"required": []string{"jinshu_id"},
		},
	}
}

// readJinshuDetail is the full metadata plus file list returned to the agent.
type readJinshuDetail struct {
	ID          int64              `json:"id"`
	From        string             `json:"from"`
	To          string             `json:"to"`
	Topic       string             `json:"topic"`
	Description string             `json:"description"`
	IsRead      bool               `json:"is_read"`
	CreatedAt   string             `json:"created_at"`
	Files       []jinshu.FileEntry `json:"files"`
}

// Execute loads a received jinshu and its file list, scoped to this agent.
func (r *ReadJinshuTool) Execute(args map[string]interface{}) (string, error) {
	jinshuID, ok := parseInt64(args["jinshu_id"])
	if !ok || jinshuID <= 0 {
		return "", fmt.Errorf("jinshu_id is required and must be a positive integer")
	}

	record, err := jinshu.GetReceived(r.personID, jinshuID)
	if err != nil {
		return "", err
	}

	files, err := jinshu.ListReceivedFiles(r.personID, jinshuID)
	if err != nil {
		return "", err
	}

	names := jinshuPersonNames([]model.Jinshu{*record})
	detail := readJinshuDetail{
		ID:          record.ID,
		From:        personName(names, record.FromPersonID),
		To:          personName(names, record.ToPersonID),
		Topic:       record.Topic,
		Description: record.Description,
		IsRead:      record.IsRead,
		CreatedAt:   record.CreatedAt.Format("2006-01-02 15:04:05"),
		Files:       files,
	}

	resp, _ := json.Marshal(detail)
	return string(resp), nil
}
