package tools

import (
	"fmt"

	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/workspace"

	servicetools "qingqiu-world-server/internal/service/tools"
)

// BashTool executes shell commands within a session workspace with sandbox isolation.
// Wraps service/tools.BashTool with Tool interface + ID-to-path translation.
type BashTool struct {
	core *servicetools.BashTool
}

// NewBashTool creates a BashTool for the given person and session.
// Workspace paths are derived via the workspace package; sandbox policy
// directory is built as {DATA_ROOT}/aac/{personID}/{sessionID}.
func NewBashTool(personID, sessionID int64) *BashTool {
	sessionRoot := workspace.GetWorkspacePath(personID, sessionID)
	outputDir := workspace.GetOutputDir(personID, sessionID)
	policyDir := workspace.GetSandboxPolicyDir(personID, sessionID)
	return &BashTool{
		core: servicetools.NewBashTool(sessionRoot, outputDir, true, policyDir),
	}
}

func (b *BashTool) Name() ToolName      { return ToolNameBash }
func (b *BashTool) Description() string { return "Execute shell commands in your working directory" }

func (b *BashTool) Schema() llm.FunctionDefinition {
	sessionRoot := b.core.RootDir()
	workspaceHint := fmt.Sprintf(" All file operations must be within %s. Do not access paths outside this directory.", sessionRoot)
	return llm.FunctionDefinition{
		Name:        b.Name().String(),
		Description: "Execute a shell command. Use this tool to run commands, manage files, and interact with the system." + workspaceHint,
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

func (b *BashTool) Execute(args map[string]interface{}) (string, error) {
	return b.core.Execute(args)
}

func (b *BashTool) CycleDetect(args map[string]interface{}, result string) CycleStatus {
	s := b.core.CycleDetect(args, result)
	return CycleStatus{Warning: s.Warning, Blocked: s.Blocked, Reason: s.Reason}
}

// SessionRoot returns the tool's workspace root path (used by Schema generation).
func (b *BashTool) SessionRoot() string { return b.core.RootDir() }
