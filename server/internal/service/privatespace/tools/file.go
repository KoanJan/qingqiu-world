package tools

import (
	"fmt"
	"qingqiu-world-server/internal/service/llm"
	servicetools "qingqiu-world-server/internal/service/tools"
)

// TextFileTool binds one shared text operation to the private-space loop.
type TextFileTool struct {
	core *servicetools.TextFileTools
	name string
}

// NewTextFileTool creates a named text reader or searcher in Agent Owned Space.
func NewTextFileTool(rootDir, workDir, name string) *TextFileTool {
	return &TextFileTool{core: servicetools.NewTextFileTools(rootDir, workDir), name: name}
}
func (t *TextFileTool) Name() string { return t.name }
func (t *TextFileTool) Description() string {
	switch t.name {
	case "read_text_lines":
		return "Read bounded numbered text lines"
	case "read_text_block":
		return "Read bounded text from a source byte offset"
	case "search_text_file":
		return "Search literal text within one file"
	default:
		return "Unknown text file operation"
	}
}
func (t *TextFileTool) Schema() llm.FunctionDefinition {
	description, parameters := servicetools.TextToolSpec(t.name)
	return llm.FunctionDefinition{Name: t.name, Description: description, Parameters: parameters}
}
func (t *TextFileTool) Execute(args map[string]interface{}) (string, error) {
	switch t.name {
	case "read_text_lines":
		return t.core.ReadTextLines(args)
	case "read_text_block":
		return t.core.ReadTextBlock(args)
	case "search_text_file":
		return t.core.SearchTextFile(args)
	default:
		return "", fmt.Errorf("unknown text file operation: %s", t.name)
	}
}

// --- Write File Tool ---

// WriteFileTool creates or overwrites files in the private-space, delegating
// path validation and atomic writing to service/tools.
type WriteFileTool struct {
	core *servicetools.WriteFileTool
}

// NewWriteFileTool creates a WriteFileTool for the given private-space paths.
func NewWriteFileTool(rootDir, workDir string) *WriteFileTool {
	return &WriteFileTool{core: servicetools.NewWriteFileTool(rootDir, workDir)}
}

func (t *WriteFileTool) Name() string { return "write_file" }
func (t *WriteFileTool) Description() string {
	return "Create or overwrite a file in your private space"
}

func (t *WriteFileTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name:        "write_file",
		Description: "Write content to a file. Creates parent directories if needed. Overwrites if the file exists.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "File path (relative to your space/ directory, or absolute within the private-space root)",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "Content to write to the file",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

// Execute translates the arg name from "path" (privatespace convention) to
// "file_path" (service/tools convention), defaults mode to "overwrite", then delegates.
func (t *WriteFileTool) Execute(args map[string]interface{}) (string, error) {
	if v, ok := args["path"]; ok {
		args["file_path"] = v
	}
	if _, ok := args["mode"]; !ok {
		args["mode"] = "overwrite"
	}
	return t.core.Execute(args)
}

// --- Edit File Tool ---

// EditFileTool makes precise text replacements in existing files, delegating
// path validation and replacement logic to service/tools.
type EditFileTool struct {
	core *servicetools.EditFileTool
}

// NewEditFileTool creates an EditFileTool for the given private-space paths.
func NewEditFileTool(rootDir, workDir string) *EditFileTool {
	return &EditFileTool{core: servicetools.NewEditFileTool(rootDir, workDir)}
}

func (t *EditFileTool) Name() string { return "edit_file" }
func (t *EditFileTool) Description() string {
	return "Make precise text replacements in existing files"
}

func (t *EditFileTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name:        "edit_file",
		Description: "Replace old_str with new_str in a file. old_str must match exactly and be unique in the file.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "File path (relative to your space/ directory, or absolute within the private-space root)",
				},
				"old_str": map[string]interface{}{
					"type":        "string",
					"description": "The exact text to find and replace",
				},
				"new_str": map[string]interface{}{
					"type":        "string",
					"description": "The replacement text",
				},
			},
			"required": []string{"path", "old_str", "new_str"},
		},
	}
}

// Execute translates the arg name from "path" (privatespace convention) to
// "file_path" (service/tools convention), defaults replace_all to false, then delegates.
func (t *EditFileTool) Execute(args map[string]interface{}) (string, error) {
	if v, ok := args["path"]; ok {
		args["file_path"] = v
	}
	if _, ok := args["replace_all"]; !ok {
		args["replace_all"] = false
	}
	return t.core.Execute(args)
}
