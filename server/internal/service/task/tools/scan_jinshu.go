package tools

import (
	"encoding/json"
	"fmt"

	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
)

// ScanJinshuTool lists the jinshu (锦书) this agent has received, newest first.
// It returns a lightweight summary (id, sender, receiver, topic, read status,
// created time); the agent uses ReadJinshuTool to inspect a specific one.
type ScanJinshuTool struct {
	personID      int64
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewScanJinshuTool creates a ScanJinshuTool for the given person.
func NewScanJinshuTool(personID int64) *ScanJinshuTool {
	return &ScanJinshuTool{personID: personID}
}

// Name returns the tool name.
func (s *ScanJinshuTool) Name() ToolName { return ToolNameScanJinshu }

// Description returns a brief description of the tool.
func (s *ScanJinshuTool) Description() string {
	return "List jinshu (锦书) you have received, newest first"
}

// Schema returns the LLM function definition for the tool.
func (s *ScanJinshuTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name: s.Name().String(),
		Description: "List the jinshu (锦书) you have received, newest first. " +
			"Returns id, sender, receiver, topic, read status, and created time. " +
			"Use read_jinshu to see the full metadata and file list of a specific jinshu.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"page": map[string]interface{}{
					"type":        "integer",
					"description": "Page number (1-based). Default: 1.",
					"default":     1,
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Number of results per page. Default: 10, max: 50.",
					"default":     10,
				},
			},
		},
	}
}

// scanJinshuEntry is the lightweight summary returned per received jinshu.
type scanJinshuEntry struct {
	ID        int64  `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Topic     string `json:"topic"`
	IsRead    bool   `json:"is_read"`
	CreatedAt string `json:"created_at"`
}

// scanJinshuResponse wraps the result list.
type scanJinshuResponse struct {
	Results []scanJinshuEntry `json:"results"`
}

// Execute lists the agent's received jinshu with offset/limit pagination.
func (s *ScanJinshuTool) Execute(args map[string]interface{}) (string, error) {
	page := 1
	if v, ok := args["page"].(float64); ok {
		page = int(v)
	}
	if page < 1 {
		page = 1
	}

	limit := 10
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
	}
	if limit < 1 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	records, err := dops.ListReceivedJinshu(s.personID, (page-1)*limit, limit)
	if err != nil {
		return "", err
	}

	names := jinshuPersonNames(records)
	entries := make([]scanJinshuEntry, 0, len(records))
	for _, r := range records {
		entries = append(entries, scanJinshuEntry{
			ID:        r.ID,
			From:      personName(names, r.FromPersonID),
			To:        personName(names, r.ToPersonID),
			Topic:     r.Topic,
			IsRead:    r.IsRead,
			CreatedAt: r.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	resp, _ := json.Marshal(scanJinshuResponse{Results: entries})
	return string(resp), nil
}

// jinshuPersonNames resolves names for every sender/receiver referenced by the
// given jinshu records. Missing names are silently left empty.
func jinshuPersonNames(records []model.Jinshu) map[int64]string {
	idSet := make(map[int64]struct{})
	for _, r := range records {
		idSet[r.FromPersonID] = struct{}{}
		idSet[r.ToPersonID] = struct{}{}
	}
	ids := make([]int64, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	names, err := dops.GetPersonNames(ids)
	if err != nil {
		return map[int64]string{}
	}
	return names
}

// personName returns the resolved name or a stable fallback.
func personName(names map[int64]string, id int64) string {
	if n := names[id]; n != "" {
		return n
	}
	return fmt.Sprintf("person_%d", id)
}
