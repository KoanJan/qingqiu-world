// Package jinshu manages the person-level jinshu (锦书) feature: directory
// layout, delivery, access, and the dedicated read loop for inspecting a
// received jinshu.
package jinshu

import (
	"context"
	"fmt"
	"os"
	"time"

	"qingqiu-world-server/internal/service/llm"
	servicetools "qingqiu-world-server/internal/service/tools"
)

// readTool is the interface for tools available in the jinshu-read loop.
type readTool interface {
	Name() string
	Description() string
	Schema() llm.FunctionDefinition
	Execute(args map[string]interface{}) (string, error)
}

// ReadJinshuFileTool reads a text file from the received jinshu directory,
// delegating path validation and line-based pagination to service/tools.
type ReadJinshuFileTool struct {
	core *servicetools.ReadFileTool
}

// NewReadJinshuFileTool creates a ReadJinshuFileTool bound to receivedDir.
func NewReadJinshuFileTool(receivedDir string) *ReadJinshuFileTool {
	return &ReadJinshuFileTool{core: servicetools.NewReadFileTool(receivedDir, receivedDir)}
}

func (t *ReadJinshuFileTool) Name() string { return "read_jinshu_file" }
func (t *ReadJinshuFileTool) Description() string {
	return "Read a text file from the received jinshu"
}

func (t *ReadJinshuFileTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name:        "read_jinshu_file",
		Description: "Read a text file from the received jinshu. Use offset/limit for large files.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]interface{}{
					"type":        "string",
					"description": "File path relative to the jinshu's received directory",
				},
				"offset": map[string]interface{}{
					"type":        "integer",
					"description": "Line number to start reading from (1-based, optional)",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of lines to read (optional)",
				},
			},
			"required": []string{"file_path"},
		},
	}
}

func (t *ReadJinshuFileTool) Execute(args map[string]interface{}) (string, error) {
	return t.core.Execute(args)
}

// maxSummarizeChars bounds the content passed to the LLM for summarization.
const maxSummarizeChars = 20000

// SummarizeJinshuFileTool reads a (possibly large) text file from the received
// jinshu and returns a concise LLM-generated summary.
type SummarizeJinshuFileTool struct {
	receivedDir string
	llmClient   *llm.ChatModel
}

// NewSummarizeJinshuFileTool creates a SummarizeJinshuFileTool bound to
// receivedDir, using llmClient for summarization.
func NewSummarizeJinshuFileTool(receivedDir string, llmClient *llm.ChatModel) *SummarizeJinshuFileTool {
	return &SummarizeJinshuFileTool{receivedDir: receivedDir, llmClient: llmClient}
}

func (t *SummarizeJinshuFileTool) Name() string { return "summarize_jinshu_file" }
func (t *SummarizeJinshuFileTool) Description() string {
	return "Summarize a text file from the received jinshu"
}

func (t *SummarizeJinshuFileTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{
		Name:        "summarize_jinshu_file",
		Description: "Read and summarize a text file from the received jinshu. Useful for large files whose full content would not fit in context.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]interface{}{
					"type":        "string",
					"description": "File path relative to the jinshu's received directory",
				},
			},
			"required": []string{"file_path"},
		},
	}
}

func (t *SummarizeJinshuFileTool) Execute(args map[string]interface{}) (string, error) {
	filePath, _ := args["file_path"].(string)
	if filePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	absPath, err := servicetools.ResolvePath(filePath, t.receivedDir, t.receivedDir)
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

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	if servicetools.IsBinaryFile(data, absPath) {
		return "", fmt.Errorf("binary file detected; only text files can be summarized")
	}

	content := string(data)
	if len(content) > maxSummarizeChars {
		content = content[:maxSummarizeChars]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	summary, err := t.llmClient.Chat(ctx, []llm.Message{
		{Role: "system", Content: "Summarize the following file content concisely, preserving all key facts, names, and conclusions. Output only the summary."},
		{Role: "user", Content: content},
	})
	if err != nil {
		return "", fmt.Errorf("summarize: %w", err)
	}
	return summary, nil
}
