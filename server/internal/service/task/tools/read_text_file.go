package tools

import (
	"fmt"

	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/workspace"

	servicetools "qingqiu-world-server/internal/service/tools"
)

// ReadTextFileTool reads text file contents with line-based pagination.
// Wraps service/tools.ReadFileTool with Tool interface + ID-to-path translation.
type ReadTextFileTool struct {
	core *servicetools.ReadFileTool
}

// NewReadTextFileTool creates a ReadTextFileTool for the given person and session.
func NewReadTextFileTool(personID, sessionID int64) *ReadTextFileTool {
	return &ReadTextFileTool{
		core: servicetools.NewReadFileTool(
			workspace.GetWorkspacePath(personID, sessionID),
			workspace.GetOutputDir(personID, sessionID),
		),
	}
}

func (r *ReadTextFileTool) Name() ToolName { return ToolNameReadTextFile }
func (r *ReadTextFileTool) Description() string {
	return "Read text file contents with line offset/limit"
}

func (r *ReadTextFileTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name:        r.Name().String(),
		Description: "Read the contents of a text file. Supports pagination via offset and limit. Rejects binary files. Use this instead of bash cat for reading files.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]interface{}{
					"type":        "string",
					"description": "Path to the file to read. Relative paths are resolved against your working directory.",
				},
				"offset": map[string]interface{}{
					"type":        "integer",
					"description": "Line number to start reading from (1-based). Default: 1.",
					"default":     1,
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum number of lines to read. Default: 200, max: 500."),
					"default":     200,
				},
			},
			"required": []string{"file_path"},
		},
	}
}

func (r *ReadTextFileTool) Execute(args map[string]interface{}) (string, error) {
	return r.core.Execute(args)
}

func (r *ReadTextFileTool) CycleDetect(args map[string]interface{}, result string) CycleStatus {
	s := r.core.CycleDetect(args, result)
	return CycleStatus{Warning: s.Warning, Blocked: s.Blocked, Reason: s.Reason}
}
