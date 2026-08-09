package tools

import (
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/workspace"

	servicetools "qingqiu-world-server/internal/service/tools"
)

// EditTextFileTool makes precise text replacements in existing files.
// Wraps service/tools.EditFileTool with Tool interface + ID-to-path translation.
type EditTextFileTool struct {
	core *servicetools.EditFileTool
}

// NewEditTextFileTool creates an EditTextFileTool for the given person and session.
func NewEditTextFileTool(personID, sessionID int64) *EditTextFileTool {
	return &EditTextFileTool{
		core: servicetools.NewEditFileTool(
			workspace.GetWorkspacePath(personID, sessionID),
			workspace.GetOutputDir(personID, sessionID),
		),
	}
}

func (e *EditTextFileTool) Name() ToolName { return ToolNameEditTextFile }
func (e *EditTextFileTool) Description() string {
	return "Make precise text replacements in existing files"
}

func (e *EditTextFileTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name:        e.Name().String(),
		Description: "Edit an existing file by replacing old_str with new_str. Uses exact substring matching — copy old_str EXACTLY from read_text_file output. Can only modify existing files; use write_text_file to create new files.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]interface{}{
					"type":        "string",
					"description": "Path to the file to edit. Relative paths are resolved against your working directory.",
				},
				"old_str": map[string]interface{}{
					"type":        "string",
					"description": "The exact text to find in the file. Must match precisely. Copy this from read_text_file output.",
				},
				"new_str": map[string]interface{}{
					"type":        "string",
					"description": "The text to replace old_str with.",
				},
				"replace_all": map[string]interface{}{
					"type":        "boolean",
					"description": "If true, replace all occurrences of old_str. If false (default), old_str must match exactly one location.",
					"default":     false,
				},
			},
			"required": []string{"file_path", "old_str", "new_str"},
		},
	}
}

func (e *EditTextFileTool) Execute(args map[string]interface{}) (string, error) {
	return e.core.Execute(args)
}

func (e *EditTextFileTool) CycleDetect(args map[string]interface{}, result string) CycleStatus {
	s := e.core.CycleDetect(args, result)
	return CycleStatus{Warning: s.Warning, Blocked: s.Blocked, Reason: s.Reason}
}
