package tools

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/sandbox"
	"qingqiu-world-server/internal/service/workspace"

	applogger "qingqiu-world-server/internal/logger"
)

// maxStdoutBytes is the maximum bytes of stdout retained after truncation.
// stdout beyond this is truncated to the tail (keeping the most recent output,
// where error messages typically appear). stderr and exit_code are never truncated.
const (
	maxStdoutBytes       = 20 * 1024 // 20KB
	defaultBashTimeoutMs = 30_000    // Default bash command timeout in milliseconds (30s)
)

// BashTool executes shell commands within a session workspace.
//
// Provides the agent with the ability to run shell commands on the local system.
// Commands are confined to the session-level workspace directory to ensure isolation.
// Supports configurable timeout and returns stdout, stderr, and exit code.
//
// Security:
//   - Commands are executed inside a platform-native sandbox (macOS Seatbelt,
//     Linux bubblewrap, Windows AppContainer)
//   - Sandbox enforces filesystem write isolation at the kernel level
//   - Falls back to plain exec if the sandbox is unavailable (availability over security)
type BashTool struct {
	personID      int64 // Person ID for workspace path derivation and sandbox policy generation
	sessionID     int64 // Session ID for workspace path derivation and sandbox policy generation
	CycleDetector       // Embedded: cycle detection on (args, result) pairs
}

// NewBashTool creates a BashTool with the given session context.
// Workspace paths (session root, output dir) are derived internally from
// personID/sessionID via the workspace package — the single source of truth
// for path layout.
func NewBashTool(personID, sessionID int64) *BashTool {
	return &BashTool{personID: personID, sessionID: sessionID}
}

// Name returns the tool name.
func (b *BashTool) Name() ToolName { return ToolNameBash }

// Description returns a brief description of the tool.
func (b *BashTool) Description() string { return "Execute shell commands in your working directory" }

// Schema returns the LLM function definition for the tool.
func (b *BashTool) Schema() llm.FunctionDefinition {
	sessionRoot := workspace.GetWorkspacePath(b.personID, b.sessionID)
	workspaceHint := fmt.Sprintf(" All file operations must be within %s. Do not access paths outside this directory.", sessionRoot)
	return llm.FunctionDefinition{
		Name:        b.Name().String(),
		Description: "Execute a shell command. Use this tool to run commands, manage files, and interact with the system." + workspaceHint,
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "The shell command to execute",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": fmt.Sprintf("Timeout in milliseconds (default: %d)", defaultBashTimeoutMs),
					"default":     defaultBashTimeoutMs,
				},
			},
			"required": []string{"command"},
		},
	}
}

// BashResult holds the structured output of a bash command execution.
type BashResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// Execute runs a bash command and returns structured output.
// Handles timeout, security checks, and returns JSON with stdout/stderr/exit_code.
func (b *BashTool) Execute(args map[string]interface{}) (string, error) {
	command, _ := args["command"].(string)
	timeoutMs := defaultBashTimeoutMs
	if t, ok := args["timeout"].(float64); ok {
		timeoutMs = int(t)
	}

	if command == "" {
		return "", fmt.Errorf("command is required")
	}

	// Build sandbox command: sandbox.Run handles platform dispatch internally
	sessionRoot := workspace.GetWorkspacePath(b.personID, b.sessionID)
	cmd, sandboxed, err := sandbox.Run(sessionRoot, b.personID, b.sessionID, []string{"bash", "-c", command})
	if err != nil {
		return "", fmt.Errorf("failed to create sandbox command: %s", err.Error())
	}
	cmd.Dir = workspace.GetOutputDir(b.personID, b.sessionID)

	applogger.Info("BashTool executing", "command", command, "timeout_ms", timeoutMs, "sandbox", sandboxed)

	timeout := time.Duration(timeoutMs) * time.Millisecond
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start command: %s", err.Error())
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		exitCode := 0
		if err != nil {
			applogger.Error("bash command execution failed", "error", err)
			exitCode = 1
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
		}
		stdoutStr := stdout.String()
		// Semantic truncation: keep tail of stdout (error messages usually at the end).
		// Happens before marshal so the JSON structure stays intact.
		if shown, truncated := TruncateTail(stdoutStr, maxStdoutBytes); truncated {
			stdoutStr = "[... earlier output omitted ...]\n" + shown + "\n" +
				Hint(len(shown), len(stdoutStr))
		}
		result, _ := json.Marshal(BashResult{
			Stdout:   stdoutStr,
			Stderr:   stderr.String(),
			ExitCode: exitCode,
		})
		return string(result), nil
	case <-timer.C:
		cmd.Process.Kill()
		return "", fmt.Errorf("command timed out after %dms", timeoutMs)
	}
}
