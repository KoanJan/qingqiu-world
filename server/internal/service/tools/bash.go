package tools

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"qingqiu-world-server/internal/service/sandbox"

	applogger "qingqiu-world-server/internal/logger"
)

const (
	maxStdoutBytes       = 20 * 1024 // 20KB
	defaultBashTimeoutMs = 30_000    // 30s
)

// BashTool executes shell commands within a workspace directory,
// optionally sandboxed.
type BashTool struct {
	rootDir   string // Workspace root for sandbox boundary
	workDir   string // Working directory for the command
	sandboxed bool   // Whether to use kernel-level sandbox
	policyDir string // Sandbox policy file directory (only used when sandboxed)
	CycleDetector
}

// RootDir returns the workspace root path.
func (b *BashTool) RootDir() string { return b.rootDir }

// NewBashTool creates a BashTool with path-based configuration.
// policyDir is only relevant when sandboxed=true.
func NewBashTool(rootDir, workDir string, sandboxed bool, policyDir string) *BashTool {
	return &BashTool{
		rootDir:   rootDir,
		workDir:   workDir,
		sandboxed: sandboxed,
		policyDir: policyDir,
	}
}

// BashResult holds the structured output of a bash command execution.
type BashResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// Execute runs a bash command and returns structured JSON output.
func (b *BashTool) Execute(args map[string]interface{}) (string, error) {
	command, _ := args["command"].(string)
	timeoutMs := defaultBashTimeoutMs
	if t, ok := args["timeout"].(float64); ok {
		timeoutMs = int(t)
	}

	if command == "" {
		return "", fmt.Errorf("command is required")
	}

	var cmd *exec.Cmd
	var sandboxed bool
	var err error

	if b.sandboxed {
		cmd, sandboxed, err = sandbox.Run(b.rootDir, b.policyDir, []string{"bash", "-c", command})
	} else {
		cmd = exec.Command("bash", "-c", command)
	}
	if err != nil {
		return "", fmt.Errorf("failed to create command: %s", err.Error())
	}
	cmd.Dir = b.workDir

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
