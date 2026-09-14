package tools

import (
	"encoding/json"
	"fmt"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
)

// RecallExperienceTool lets the agent read the full content of a single
// private experience by its ID.
//
// This is the second step of progressive disclosure: after scan_my_experience
// returns a lightweight list, the agent uses this tool to read the complete
// content of only the experiences it judges relevant. The full content is
// returned without truncation — a truncated experience is unusable.
type RecallExperienceTool struct {
	personID      int64
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewRecallExperienceTool creates a RecallExperienceTool for the given person.
func NewRecallExperienceTool(personID int64) *RecallExperienceTool {
	return &RecallExperienceTool{personID: personID}
}

// Name returns the tool name.
func (r *RecallExperienceTool) Name() ToolName { return ToolNameRecallMyExperience }

// Description returns a brief description of the tool.
func (r *RecallExperienceTool) Description() string {
	return "Read the full content of a specific experience by its exp_id"
}

// Schema returns the LLM function definition for the tool.
func (r *RecallExperienceTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name: r.Name().String(),
		Description: "Read the full content of one of your private experiences by its exp_id. " +
			"Returns all fields: id, title, description, when_to_use, guidelines, pitfalls, and procedure. " +
			"The content is never truncated — you receive the complete experience text.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"exp_id": map[string]interface{}{
					"type":        "integer",
					"description": "The experience ID returned by scan_my_experience",
				},
			},
			"required": []string{"exp_id"},
		},
	}
}

// recallDetail is the full experience content returned to the agent.
type recallDetail struct {
	ExpID       int64  `json:"exp_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	WhenToUse   string `json:"when_to_use"`
	Guidelines  string `json:"guidelines"`
	Pitfalls    string `json:"pitfalls"`
	Procedure   string `json:"procedure"`
}

// Execute loads a single experience by ID, scoped to this agent.
// The full content is returned without truncation.
func (r *RecallExperienceTool) Execute(args map[string]interface{}) (string, error) {
	expID, ok := parseInt64(args["exp_id"])
	if !ok || expID <= 0 {
		return "", fmt.Errorf("exp_id is required and must be a positive integer")
	}

	var exp model.AgentExperience
	if err := database.DB.Where("id = ? AND person_id = ?", expID, r.personID).First(&exp).Error; err != nil {
		return "", fmt.Errorf("experience not found")
	}

	detail := recallDetail{
		ExpID:       exp.ID,
		Title:       exp.Title,
		Description: exp.Description,
		WhenToUse:   exp.WhenToUse,
		Guidelines:  exp.Guidelines,
		Pitfalls:    exp.Pitfalls,
		Procedure:   exp.Procedure,
	}

	resp, _ := json.Marshal(detail)
	return string(resp), nil
}

// parseInt64 extracts an int64 from a tool argument.
// JSON numbers come through as float64, so we handle both int and float64.
func parseInt64(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}
