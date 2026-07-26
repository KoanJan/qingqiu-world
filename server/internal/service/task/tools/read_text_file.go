package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"qingqiu-world-server/internal/service/llm"
)

// ReadTextFileTool reads text file contents with line-based pagination.
//
// Provides the agent with a safe, structured way to read file contents
// without the escaping issues of bash cat. Supports offset/limit for
// pagination and rejects binary files.
//
// Security:
//   - Path traversal outside the session workspace is blocked
//   - Access to .meta directory is blocked
//   - Binary files are rejected (extension blacklist + null-byte sniffing)
//   - Files larger than 10MB are rejected
type ReadTextFileTool struct {
	personID      int64
	sessionID     int64
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewReadTextFileTool creates a ReadTextFileTool bound to the given person and session.
// The personID and sessionID are used to resolve the session workspace for path validation.
func NewReadTextFileTool(personID, sessionID int64) *ReadTextFileTool {
	return &ReadTextFileTool{personID: personID, sessionID: sessionID}
}

// readResultDefaults defines pagination bounds for read_text_file.
const (
	defaultReadLimit = 200
	maxReadLimit     = 500
)

// Name returns the tool name.
func (r *ReadTextFileTool) Name() ToolName { return ToolNameReadTextFile }

// Description returns a brief description of the tool.
func (r *ReadTextFileTool) Description() string {
	return "Read text file contents with line offset/limit"
}

// Schema returns the LLM function definition for the tool.
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
					"description": fmt.Sprintf("Maximum number of lines to read. Default: %d, max: %d.", defaultReadLimit, maxReadLimit),
					"default":     defaultReadLimit,
				},
			},
			"required": []string{"file_path"},
		},
	}
}

// readTextFileResult is the JSON return structure for read_text_file.
type readTextFileResult struct {
	FilePath      string `json:"file_path"`
	TotalLines    int    `json:"total_lines"`
	FileSizeBytes int64  `json:"file_size_bytes"`
	Content       string `json:"content"`
}

// Execute reads a text file and returns its content with metadata.
func (r *ReadTextFileTool) Execute(args map[string]interface{}) (string, error) {
	filePath, _ := args["file_path"].(string)
	if filePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	offset := 1
	if v, ok := args["offset"].(float64); ok {
		offset = int(v)
	}
	if offset < 1 {
		offset = 1
	}

	limit := defaultReadLimit
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
	}
	if limit < 1 {
		limit = defaultReadLimit
	}
	if limit > maxReadLimit {
		limit = maxReadLimit
	}

	absPath, err := resolvePath(filePath, r.personID, r.sessionID)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found: %s", filePath)
		}
		return "", fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory, not a file: %s", filePath)
	}
	if info.Size() > maxFileBytes {
		return "", fmt.Errorf("file is too large (%d bytes, max %d). Use offset/limit to read in chunks", info.Size(), maxFileBytes)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	if isBinaryFile(data, absPath) {
		return "", fmt.Errorf("binary file detected. read_text_file only supports text files")
	}

	content := string(data)

	// Calculate total_lines: count newlines + 1, but empty file = 0
	totalLines := 0
	if len(content) > 0 {
		totalLines = strings.Count(content, "\n") + 1
	}

	// Split by \n for pagination
	lines := strings.Split(content, "\n")

	// Extract the requested page
	var pageContent string
	startIdx := offset - 1
	if startIdx >= len(lines) {
		// offset beyond file length — return empty content
		pageContent = ""
	} else {
		endIdx := startIdx + limit
		if endIdx > len(lines) {
			endIdx = len(lines)
		}
		pageContent = strings.Join(lines[startIdx:endIdx], "\n")
	}

	result := readTextFileResult{
		FilePath:      absPath,
		TotalLines:    totalLines,
		FileSizeBytes: info.Size(),
		Content:       pageContent,
	}

	jsonBytes, _ := json.Marshal(result)
	return string(jsonBytes), nil
}
