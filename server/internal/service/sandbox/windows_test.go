package sandbox

import (
	applogger "qingqiu-world-server/internal/logger"
	"runtime"
	"testing"
)

// requireWindows skips the test if not running on Windows.
func requireWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("test requires Windows")
	}
}

// TestRunWindows_EmptyCmd verifies that Run returns an error when given an empty
// command slice on Windows.
func TestRunWindows_EmptyCmd(t *testing.T) {
	requireWindows(t)

	_, _, err := Run(`C:\tmp\ws`, 1, 1, []string{})
	if err == nil {
		t.Error("expected error for empty cmd")
	}
}

// TestRunWindows_FallbackExec verifies that Run passes commands through to plain
// os/exec on Windows.
func TestRunWindows_FallbackExec(t *testing.T) {
	requireWindows(t)

	cmd, _, err := Run(`C:\tmp\ws`, 1, 1, []string{"cmd", "/c", "echo", "hello"})
	if err != nil {
		applogger.Error("sandbox test: Run failed on windows fallback exec", "error", err)
		t.Fatalf("Run() returned error: %v", err)
	}
	if cmd == nil {
		t.Fatal("expected non-nil *exec.Cmd")
	}
	// In current fallback mode, the command should be passed through
	if cmd.Path != "cmd" {
		t.Errorf("expected 'cmd', got %q", cmd.Path)
	}
}

// TestRunWindows_ArgsPreserved verifies that command arguments are correctly
// preserved when Run is called on Windows.
func TestRunWindows_ArgsPreserved(t *testing.T) {
	requireWindows(t)

	cmd, _, err := Run(`C:\tmp\ws`, 1, 1, []string{"powershell", "-Command", "Write-Host test"})
	if err != nil {
		applogger.Error("sandbox test: Run failed on windows args test", "error", err)
		t.Fatalf("Run() returned error: %v", err)
	}
	if len(cmd.Args) < 3 {
		t.Errorf("expected at least 3 args, got %d: %v", len(cmd.Args), cmd.Args)
	}
}
