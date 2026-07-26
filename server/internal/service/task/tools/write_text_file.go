package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"qingqiu-world-server/internal/service/llm"
)

// WriteTextFileTool creates, overwrites, or appends to text files atomically.
//
// Provides the agent with a safe way to write file contents without the
// escaping issues of bash echo/heredoc. Supports two modes:
//   - overwrite: atomically replace the entire file content
//   - append: append content to the end of an existing file
//
// Security:
//   - Path traversal outside the session workspace is blocked
//   - Access to .meta directory is blocked
//   - Writing to directories is blocked
//
// The overwrite mode uses atomic write (temp file + rename) to ensure
// the file is never in a half-written state.
type WriteTextFileTool struct {
	personID      int64
	sessionID     int64
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewWriteTextFileTool creates a WriteTextFileTool bound to the given person and session.
func NewWriteTextFileTool(personID, sessionID int64) *WriteTextFileTool {
	return &WriteTextFileTool{personID: personID, sessionID: sessionID}
}

// Name returns the tool name.
func (w *WriteTextFileTool) Name() ToolName { return ToolNameWriteTextFile }

// Description returns a brief description of the tool.
func (w *WriteTextFileTool) Description() string {
	return "Create, overwrite, or append to text files"
}

// Schema returns the LLM function definition for the tool.
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

// writeTextFileResult is the JSON return structure for write_text_file.
type writeTextFileResult struct {
	FilePath      string `json:"file_path"`
	Type          string `json:"type"`
	BytesWritten  int    `json:"bytes_written"`
	PreviousBytes int64  `json:"previous_bytes"`
}

// Execute writes content to a file in the specified mode.
func (w *WriteTextFileTool) Execute(args map[string]interface{}) (string, error) {
	filePath, _ := args["file_path"].(string)
	if filePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	content, _ := args["content"].(string)

	mode, _ := args["mode"].(string)
	if mode != "overwrite" && mode != "append" {
		return "", fmt.Errorf("mode must be 'overwrite' or 'append'")
	}

	absPath, err := resolvePath(filePath, w.personID, w.sessionID)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	// Check if file exists and get previous size
	var previousBytes int64
	var resultType string
	if info, err := os.Stat(absPath); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("path is a directory, cannot write: %s", filePath)
		}
		previousBytes = info.Size()
		resultType = "update"
	} else if os.IsNotExist(err) {
		resultType = "create"
	} else {
		return "", fmt.Errorf("stat file: %w", err)
	}

	// Create parent directories if needed
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create directories: %w", err)
	}

	// Execute write based on mode
	switch mode {
	case "overwrite":
		if err := atomicWrite(absPath, content); err != nil {
			return "", fmt.Errorf("write file: %w", err)
		}
	case "append":
		f, err := os.OpenFile(absPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return "", fmt.Errorf("open file for append: %w", err)
		}
		if _, err := f.WriteString(content); err != nil {
			f.Close()
			return "", fmt.Errorf("append to file: %w", err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("close file: %w", err)
		}
	}

	result := writeTextFileResult{
		FilePath:      absPath,
		Type:          resultType,
		BytesWritten:  len(content),
		PreviousBytes: previousBytes,
	}

	jsonBytes, _ := json.Marshal(result)
	return string(jsonBytes), nil
}
