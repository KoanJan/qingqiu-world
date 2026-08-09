package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// EditFileTool makes precise text replacements in existing files.
// Paths are validated against rootDir and resolved relative to workDir.
type EditFileTool struct {
	rootDir string
	workDir string
	CycleDetector
}

// NewEditFileTool creates an EditFileTool with path-based configuration.
func NewEditFileTool(rootDir, workDir string) *EditFileTool {
	return &EditFileTool{rootDir: rootDir, workDir: workDir}
}

// editFileResult is the JSON return structure for edit_file.
type editFileResult struct {
	FilePath      string `json:"file_path"`
	Type          string `json:"type"`
	Occurrences   int    `json:"occurrences"`
	BytesWritten  int    `json:"bytes_written"`
	PreviousBytes int    `json:"previous_bytes"`
	MatchMethod   string `json:"match_method"`
	Reason        string `json:"reason"`
}

// Execute performs a text replacement in an existing file.
func (e *EditFileTool) Execute(args map[string]interface{}) (string, error) {
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

	absPath, err := ResolvePath(filePath, e.rootDir, e.workDir)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found. edit_text_file can only modify existing files")
		}
		return "", fmt.Errorf("read file: %w", err)
	}

	if IsBinaryFile(data, absPath) {
		return "", fmt.Errorf("binary file detected. edit_text_file only supports text files")
	}

	content := string(data)
	previousBytes := len(content)

	if oldStr == newStr {
		jsonBytes, _ := json.Marshal(editFileResult{
			FilePath:      absPath,
			Type:          "update",
			Occurrences:   0,
			BytesWritten:  0,
			PreviousBytes: previousBytes,
			MatchMethod:   "substring",
			Reason:        "old_str and new_str are identical, no changes made",
		})
		return string(jsonBytes), nil
	}

	matchCount := strings.Count(content, oldStr)
	if matchCount == 0 {
		return "", fmt.Errorf("old_str not found in file")
	}

	if matchCount > 1 && !replaceAll {
		return "", fmt.Errorf("found %d matches for old_str. Provide more surrounding context to make it unique, or set replace_all: true", matchCount)
	}

	var modifiedContent string
	var occurrences int
	if replaceAll {
		modifiedContent = strings.ReplaceAll(content, oldStr, newStr)
		occurrences = matchCount
	} else {
		modifiedContent = strings.Replace(content, oldStr, newStr, 1)
		occurrences = 1
	}

	if err := AtomicWrite(absPath, modifiedContent); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	result := editFileResult{
		FilePath:      absPath,
		Type:          "update",
		Occurrences:   occurrences,
		BytesWritten:  len(modifiedContent),
		PreviousBytes: previousBytes,
		MatchMethod:   "substring",
	}

	jsonBytes, _ := json.Marshal(result)
	return string(jsonBytes), nil
}
