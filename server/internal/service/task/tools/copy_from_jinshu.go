package tools

import (
	"fmt"
	"strings"

	"qingqiu-world-server/internal/service/jinshu"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/workspace"

	servicetools "qingqiu-world-server/internal/service/tools"
)

// CopyFromJinshuTool copies the files delivered by a received jinshu into the
// agent's session output directory.
type CopyFromJinshuTool struct {
	personID      int64
	sessionID     int64
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewCopyFromJinshuTool creates a CopyFromJinshuTool for the given person and
// session. sessionID locates the output/ directory that receives the copies.
func NewCopyFromJinshuTool(personID, sessionID int64) *CopyFromJinshuTool {
	return &CopyFromJinshuTool{
		personID:  personID,
		sessionID: sessionID,
	}
}

// Name returns the tool name.
func (c *CopyFromJinshuTool) Name() ToolName { return ToolNameCopyFromJinshu }

// Description returns a brief description of the tool.
func (c *CopyFromJinshuTool) Description() string {
	return "Copy files from a received jinshu into your output directory"
}

// Schema returns the LLM function definition for the tool.
func (c *CopyFromJinshuTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name: c.Name().String(),
		Description: "Copy the files delivered by a received jinshu into a directory under your " +
			"output/ directory. The target directory is created if it does not exist, and existing " +
			"files with the same name are overwritten.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"jinshu_id": map[string]interface{}{
					"type":        "integer",
					"description": "The id of the received jinshu (from scan_jinshu).",
				},
				"target_relative_dir": map[string]interface{}{
					"type":        "string",
					"description": "Directory under your output/ directory to copy the files into.",
				},
			},
			"required": []string{"jinshu_id", "target_relative_dir"},
		},
	}
}

// Execute copies a received jinshu's files into the resolved target directory.
func (c *CopyFromJinshuTool) Execute(args map[string]interface{}) (string, error) {
	jinshuID, ok := parseInt64(args["jinshu_id"])
	if !ok || jinshuID <= 0 {
		return "", fmt.Errorf("jinshu_id is required and must be a positive integer")
	}

	targetRel, ok := args["target_relative_dir"].(string)
	if !ok || strings.TrimSpace(targetRel) == "" {
		return "", fmt.Errorf("target_relative_dir must be a non-empty string")
	}

	// Verify the jinshu is addressed to this agent before touching the filesystem.
	if _, err := jinshu.GetReceived(c.personID, jinshuID); err != nil {
		return "", err
	}

	outputDir := workspace.GetOutputDir(c.personID, c.sessionID)
	targetDir, err := servicetools.ResolvePath(targetRel, outputDir, outputDir)
	if err != nil {
		return "", fmt.Errorf("invalid target_relative_dir %q: %w", targetRel, err)
	}

	copied, err := jinshu.CopyReceivedTo(c.personID, jinshuID, targetDir)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Copied %d item(s) from jinshu #%d into %s: %s",
		len(copied), jinshuID, targetRel, strings.Join(copied, ", ")), nil
}
