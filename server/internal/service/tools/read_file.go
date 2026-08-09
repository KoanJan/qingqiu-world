package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const (
	defaultReadLimit = 200
	maxReadLimit     = 500
)

// ReadFileTool reads text file contents with line-based pagination.
// Paths are validated against rootDir and resolved relative to workDir.
type ReadFileTool struct {
	rootDir string // Workspace root for path boundary validation
	workDir string // Base directory for relative path resolution
	CycleDetector
}

// NewReadFileTool creates a ReadFileTool with path-based configuration.
func NewReadFileTool(rootDir, workDir string) *ReadFileTool {
	return &ReadFileTool{rootDir: rootDir, workDir: workDir}
}

// readFileResult is the JSON return structure for read_file.
type readFileResult struct {
	FilePath      string `json:"file_path"`
	TotalLines    int    `json:"total_lines"`
	FileSizeBytes int64  `json:"file_size_bytes"`
	Content       string `json:"content"`
}

// Execute reads a text file and returns its content with metadata.
func (r *ReadFileTool) Execute(args map[string]interface{}) (string, error) {
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

	absPath, err := ResolvePath(filePath, r.rootDir, r.workDir)
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

	if IsBinaryFile(data, absPath) {
		return "", fmt.Errorf("binary file detected. read_text_file only supports text files")
	}

	content := string(data)

	totalLines := 0
	if len(content) > 0 {
		totalLines = strings.Count(content, "\n") + 1
	}

	lines := strings.Split(content, "\n")

	var pageContent string
	startIdx := offset - 1
	if startIdx >= len(lines) {
		pageContent = ""
	} else {
		endIdx := startIdx + limit
		if endIdx > len(lines) {
			endIdx = len(lines)
		}
		pageContent = strings.Join(lines[startIdx:endIdx], "\n")
	}

	result := readFileResult{
		FilePath:      absPath,
		TotalLines:    totalLines,
		FileSizeBytes: info.Size(),
		Content:       pageContent,
	}

	jsonBytes, _ := json.Marshal(result)
	return string(jsonBytes), nil
}
