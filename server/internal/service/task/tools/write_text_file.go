package tools

import (
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/workspace"

	servicetools "qingqiu-world-server/internal/service/tools"
)

// WriteTextFileTool creates, overwrites, or appends to text files atomically.
// Wraps service/tools.WriteFileTool with Tool interface + ID-to-path translation.
type WriteTextFileTool struct {
	core *servicetools.WriteFileTool
}

// NewWriteTextFileTool creates a WriteTextFileTool for the given person and session.
func NewWriteTextFileTool(personID, sessionID int64) *WriteTextFileTool {
	return &WriteTextFileTool{
		core: servicetools.NewWriteFileTool(
			workspace.GetWorkspacePath(personID, sessionID),
			workspace.GetOutputDir(personID, sessionID),
		),
	}
}

func (w *WriteTextFileTool) Name() ToolName      { return ToolNameWriteTextFile }
func (w *WriteTextFileTool) Description() string { return "Create, overwrite, or append to text files" }

func (w *WriteTextFileTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name:        w.Name().String(),
		Description: "Write content to a text file. Supports overwrite (replace entire file) and append (add to end) modes. Use this instead of bash echo/heredoc for writing files.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]interface{}{
					"type":        "string",
					"description": "Path to the file to write. Relative paths are resolved against your working directory. Parent directories are created automatically.",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "The content to write to the file.",
				},
				"mode": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"overwrite", "append"},
					"description": "Write mode: 'overwrite' replaces the entire file content; 'append' adds content to the end of an existing file.",
				},
			},
			"required": []string{"file_path", "content", "mode"},
		},
	}
}

func (w *WriteTextFileTool) Execute(args map[string]interface{}) (string, error) {
	return w.core.Execute(args)
}

func (w *WriteTextFileTool) CycleDetect(args map[string]interface{}, result string) CycleStatus {
	s := w.core.CycleDetect(args, result)
	return CycleStatus{Warning: s.Warning, Blocked: s.Blocked, Reason: s.Reason}
}
