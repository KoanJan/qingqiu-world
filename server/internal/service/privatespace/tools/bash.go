package tools

import (
	"fmt"
	"path/filepath"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/service/llm"
	servicetools "qingqiu-world-server/internal/service/tools"
)

// BashTool executes shell commands sandboxed within the private-space directory.
// Wraps service/tools.BashTool with sandbox enabled.
type BashTool struct {
	core *servicetools.BashTool
}

// NewBashTool creates a sandboxed BashTool for the private-space.
// rootDir is the sandbox boundary; workDir is the bash working directory.
// Sandbox policy files are stored in {DATA_ROOT}/aac/{personID}/private/.
func NewBashTool(personID int64, rootDir, workDir string) *BashTool {
	policyDir := filepath.Join(config.Get().GetDataRoot(), "aac",
		fmt.Sprintf("%d", personID), "private")
	return &BashTool{
		core: servicetools.NewBashTool(rootDir, workDir, true, policyDir),
	}
}

func (t *BashTool) Name() string { return "bash" }
func (t *BashTool) Description() string {
	return "Execute shell commands in your private-space directory"
}

func (t *BashTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name:        "bash",
		Description: "Execute a shell command. Working directory is your space/ directory. Sandboxed for security.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "The shell command to execute",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Timeout in milliseconds (default: 30000)",
					"default":     30000,
				},
			},
			"required": []string{"command"},
		},
	}
}

func (t *BashTool) Execute(args map[string]interface{}) (string, error) {
	return t.core.Execute(args)
}
