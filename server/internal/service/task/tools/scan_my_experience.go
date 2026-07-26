package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"qingqiu-world-server/internal/service/experience"
	"qingqiu-world-server/internal/service/llm"

	applogger "qingqiu-world-server/internal/logger"
)

// Retrieval parameters for experience scanning.
// Slightly more generous than the old system-prompt injection defaults
// (topN=3, minScore=0.4) because the agent crafts its own keyword and
// should have room to choose which results to recall in full.
const (
	scanExperienceTopN     = 5
	scanExperienceMinScore = 0.3
)

// ScanExperienceTool lets the agent search its private experiences by keyword.
//
// This implements progressive disclosure: the agent first scans a lightweight
// summary list (id, title, description, when_to_use), then uses
// RecallExperienceTool to read the full content of only the relevant ones.
// This replaces the old approach of injecting all experiences into the
// system prompt, which polluted the context and could not be self-corrected.
type ScanExperienceTool struct {
	personID      int64
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewScanExperienceTool creates a ScanExperienceTool for the given person.
func NewScanExperienceTool(personID int64) *ScanExperienceTool {
	return &ScanExperienceTool{personID: personID}
}

// Name returns the tool name.
func (s *ScanExperienceTool) Name() ToolName { return ToolNameScanMyExperience }

// Description returns a brief description of the tool.
func (s *ScanExperienceTool) Description() string {
	return "Search your past experiences by keyword to find relevant lessons"
}

// Schema returns the LLM function definition for the tool.
func (s *ScanExperienceTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name: s.Name().String(),
		Description: "Search your private experiences (lessons learned from past tasks) by keyword. " +
			"Returns a list of matching experiences with id, title, description, and when_to_use. " +
			"Use recall_my_experience with the exp_id to read the full content of a specific experience.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"keyword": map[string]interface{}{
					"type":        "string",
					"description": "Search keyword or phrase describing what you're looking for",
				},
			},
			"required": []string{"keyword"},
		},
	}
}

// scanResultEntry is the lightweight summary returned per experience.
type scanResultEntry struct {
	ExpID       int64  `json:"exp_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	WhenToUse   string `json:"when_to_use"`
}

// scanResponse wraps the result list.
type scanResponse struct {
	Results []scanResultEntry `json:"results"`
	Error   string            `json:"error,omitempty"`
}

// Execute searches the agent's private experiences by semantic similarity
// to the keyword and returns a lightweight summary list.
func (s *ScanExperienceTool) Execute(args map[string]interface{}) (string, error) {
	keyword, _ := args["keyword"].(string)
	if keyword == "" {
		return "", fmt.Errorf("keyword is required")
	}

	results, err := experience.SearchExperiences(
		context.Background(), s.personID, keyword,
		scanExperienceTopN, scanExperienceMinScore,
	)
	if err != nil {
		applogger.Error("scan_my_experience failed",
			"person_id", s.personID,
			"keyword", keyword,
			"error", err,
		)
		return "", fmt.Errorf("search failed: %s", err.Error())
	}

	entries := make([]scanResultEntry, 0, len(results))
	for _, r := range results {
		entries = append(entries, scanResultEntry{
			ExpID:       r.Experience.ID,
			Title:       r.Experience.Title,
			Description: r.Experience.Description,
			WhenToUse:   r.Experience.WhenToUse,
		})
	}

	resp, _ := json.Marshal(scanResponse{Results: entries})
	return string(resp), nil
}
