package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileTool creates, overwrites, or appends to text files atomically.
// Paths are validated against rootDir and resolved relative to workDir.
type WriteFileTool struct {
	rootDir string
	workDir string
	CycleDetector
}

// NewWriteFileTool creates a WriteFileTool with path-based configuration.
func NewWriteFileTool(rootDir, workDir string) *WriteFileTool {
	return &WriteFileTool{rootDir: rootDir, workDir: workDir}
}

// writeFileResult is the JSON return structure for write_file.
type writeFileResult struct {
	FilePath      string `json:"file_path"`
	Type          string `json:"type"`
	BytesWritten  int    `json:"bytes_written"`
	PreviousBytes int64  `json:"previous_bytes"`
}

// Execute writes content to a file in the specified mode.
func (w *WriteFileTool) Execute(args map[string]interface{}) (string, error) {
	filePath, _ := args["file_path"].(string)
	if filePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	content, _ := args["content"].(string)

	mode, _ := args["mode"].(string)
	if mode != "overwrite" && mode != "append" {
		return "", fmt.Errorf("mode must be 'overwrite' or 'append'")
	}

	absPath, err := ResolvePath(filePath, w.rootDir, w.workDir)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

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

	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create directories: %w", err)
	}

	switch mode {
	case "overwrite":
		if err := AtomicWrite(absPath, content); err != nil {
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

	result := writeFileResult{
		FilePath:      absPath,
		Type:          resultType,
		BytesWritten:  len(content),
		PreviousBytes: previousBytes,
	}

	jsonBytes, _ := json.Marshal(result)
	return string(jsonBytes), nil
}
