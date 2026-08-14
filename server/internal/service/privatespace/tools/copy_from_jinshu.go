package tools

import (
	"fmt"
	"strings"

	"qingqiu-world-server/internal/service/jinshu"
	"qingqiu-world-server/internal/service/llm"

	servicetools "qingqiu-world-server/internal/service/tools"
)

// CopyFromJinshuTool copies the files delivered by a received jinshu into the
// agent's private-space working directory.
type CopyFromJinshuTool struct {
	personID int64
	workDir  string
}

// NewCopyFromJinshuTool creates a CopyFromJinshuTool for the given person and
// private space. rootDir is kept for API symmetry; the target is workDir.
func NewCopyFromJinshuTool(personID int64, rootDir, workDir string) *CopyFromJinshuTool {
	return &CopyFromJinshuTool{
		personID: personID,
		workDir:  workDir,
	}
}

func (t *CopyFromJinshuTool) Name() string { return "copy_from_jinshu" }
func (t *CopyFromJinshuTool) Description() string {
	return "Copy files from a received jinshu into your private-space working directory"
}

func (t *CopyFromJinshuTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name: "copy_from_jinshu",
		Description: "Copy the files delivered by a received jinshu into a directory under your " +
			"private-space working directory. The target directory is created if it does not exist, " +
			"and existing files with the same name are overwritten.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"jinshu_id": map[string]interface{}{
					"type":        "integer",
					"description": "The id of the received jinshu.",
				},
				"target_relative_dir": map[string]interface{}{
					"type":        "string",
					"description": "Directory under your private-space working directory to copy the files into.",
				},
			},
			"required": []string{"jinshu_id", "target_relative_dir"},
		},
	}
}

// Execute copies a received jinshu's files into the resolved target directory.
func (t *CopyFromJinshuTool) Execute(args map[string]interface{}) (string, error) {
	jinshuID, ok := parseInt64(args["jinshu_id"])
	if !ok || jinshuID <= 0 {
		return "", fmt.Errorf("jinshu_id is required and must be a positive integer")
	}

	targetRel, ok := args["target_relative_dir"].(string)
	if !ok || strings.TrimSpace(targetRel) == "" {
		return "", fmt.Errorf("target_relative_dir must be a non-empty string")
	}

	// Verify the jinshu is addressed to this agent before touching the filesystem.
	if _, err := jinshu.GetReceived(t.personID, jinshuID); err != nil {
		return "", err
	}

	targetDir, err := servicetools.ResolvePath(targetRel, t.workDir, t.workDir)
	if err != nil {
		return "", fmt.Errorf("invalid target_relative_dir %q: %w", targetRel, err)
	}

	copied, err := jinshu.CopyReceivedTo(t.personID, jinshuID, targetDir)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Copied %d item(s) from jinshu #%d into %s: %s",
		len(copied), jinshuID, targetRel, strings.Join(copied, ", ")), nil
}

// parseInt64 extracts an int64 from a tool argument. JSON numbers arrive as
// float64, but int and int64 are also accepted for caller convenience.
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
