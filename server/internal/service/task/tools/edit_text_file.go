package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"qingqiu-world-server/internal/service/llm"
)

// EditTextFileTool makes precise text replacements in existing files.
//
// Provides the agent with a safe way to modify files without the escaping
// issues of bash sed. Uses exact substring matching — the agent must copy
// old_str exactly from read_text_file output.
//
// Security:
//   - Path traversal outside the session workspace is blocked
//   - Access to .meta directory is blocked
//   - Binary files are rejected
//   - Can only modify existing files (use write_text_file to create)
//
// Matching strategy:
//   - Exact substring match only, no normalization
//   - If old_str matches multiple locations and replace_all is false, returns an error
//   - If replace_all is true, replaces all occurrences
type EditTextFileTool struct {
	personID      int64
	sessionID     int64
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewEditTextFileTool creates an EditTextFileTool bound to the given person and session.
func NewEditTextFileTool(personID, sessionID int64) *EditTextFileTool {
	return &EditTextFileTool{personID: personID, sessionID: sessionID}
}

// Name returns the tool name.
func (e *EditTextFileTool) Name() ToolName { return ToolNameEditTextFile }

// Description returns a brief description of the tool.
func (e *EditTextFileTool) Description() string {
	return "Make precise text replacements in existing files"
}

// Schema returns the LLM function definition for the tool.
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
					"description": "The exact text to find in the file. Must match precisely, including indentation and special characters. Copy this from read_text_file output.",
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

// editTextFileResult is the JSON return structure for edit_text_file.
type editTextFileResult struct {
	FilePath      string `json:"file_path"`
	Type          string `json:"type"`
	Occurrences   int    `json:"occurrences"`
	BytesWritten  int    `json:"bytes_written"`
	PreviousBytes int    `json:"previous_bytes"`
	MatchMethod   string `json:"match_method"`
	Reason        string `json:"reason"`
}

// Execute performs a text replacement in an existing file.
func (e *EditTextFileTool) Execute(args map[string]interface{}) (string, error) {
	filePath, _ := args["file_path"].(string)
	if filePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	oldStr, _ := args["old_str"].(string)
	if oldStr == "" {
		return "", fmt.Errorf("old_str is required and cannot be empty")
	}

	newStr, _ := args["new_str"].(string)

	replaceAll := false
	if v, ok := args["replace_all"].(bool); ok {
		replaceAll = v
	}

	absPath, err := resolvePath(filePath, e.personID, e.sessionID)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found. edit_text_file can only modify existing files; use write_text_file to create new files")
		}
		return "", fmt.Errorf("read file: %w", err)
	}

	if isBinaryFile(data, absPath) {
		return "", fmt.Errorf("binary file detected. edit_text_file only supports text files")
	}

	content := string(data)
	previousBytes := len(content)

	// Short-circuit: old_str == new_str, no changes needed
	if oldStr == newStr {
		result := editTextFileResult{
			FilePath:      absPath,
			Type:          "update",
			Occurrences:   0,
			BytesWritten:  0,
			PreviousBytes: previousBytes,
			MatchMethod:   "substring",
			Reason:        "old_str and new_str are identical, no changes made",
		}
		jsonBytes, _ := json.Marshal(result)
		return string(jsonBytes), nil
	}

	// Count matches
	matchCount := strings.Count(content, oldStr)
	if matchCount == 0 {
		return "", fmt.Errorf("old_str not found in file. Use read_text_file to verify current content and try again with exact text from the file")
	}

	if matchCount > 1 && !replaceAll {
		return "", fmt.Errorf("found %d matches for old_str. Provide more surrounding context to make it unique, or set replace_all: true", matchCount)
	}

	// Execute replacement
	var modifiedContent string
	var occurrences int
	if replaceAll {
		modifiedContent = strings.ReplaceAll(content, oldStr, newStr)
		occurrences = matchCount
	} else {
		modifiedContent = strings.Replace(content, oldStr, newStr, 1)
		occurrences = 1
	}

	// Atomic write
	if err := atomicWrite(absPath, modifiedContent); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	result := editTextFileResult{
		FilePath:      absPath,
		Type:          "update",
		Occurrences:   occurrences,
		BytesWritten:  len(modifiedContent),
		PreviousBytes: previousBytes,
		MatchMethod:   "substring",
		Reason:        "",
	}

	jsonBytes, _ := json.Marshal(result)
	return string(jsonBytes), nil
}
