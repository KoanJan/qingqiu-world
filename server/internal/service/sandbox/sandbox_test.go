package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	applogger "qingqiu-world-server/internal/logger"
)

// TestMain initializes the logger so sandbox execution tests can log without panicking.
func TestMain(m *testing.M) {
	applogger.Init()
	os.Exit(m.Run())
}

// TestRun_EmptyCmd tests that Run returns an error for an empty command slice.
func TestRun_EmptyCmd(t *testing.T) {
	_, _, err := Run("/tmp/ws", 1, 1, []string{})
	if err == nil {
		t.Error("expected error for empty cmd")
	}
	if err.Error() != "sandbox: cmd is empty" {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestFallbackExec_NonEmpty tests fallbackExec with valid arguments.
func TestFallbackExec_NonEmpty(t *testing.T) {
	cmd := fallbackExec([]string{"echo", "hello"})
	// exec.Command resolves to the absolute path on some platforms
	if cmd.Path != "echo" && filepath.Base(cmd.Path) != "echo" {
		t.Errorf("expected 'echo', got %q", cmd.Path)
	}
	if len(cmd.Args) != 2 || cmd.Args[1] != "hello" {
		t.Errorf("expected [echo hello], got %v", cmd.Args)
	}
}

// TestFallbackExec_Empty tests fallbackExec with empty args.
func TestFallbackExec_Empty(t *testing.T) {
	cmd := fallbackExec([]string{})
	if cmd.Path != "true" && filepath.Base(cmd.Path) != "true" {
		t.Errorf("expected 'true' for empty cmd, got %q", cmd.Path)
	}
}

// TestFallbackExec_Integration verifies that the returned *exec.Cmd can actually run.
func TestFallbackExec_Integration(t *testing.T) {
	cmd := fallbackExec([]string{"true"})
	if err := cmd.Run(); err != nil {
		applogger.Error("sandbox test: fallback exec of 'true' failed", "error", err)
		t.Errorf("fallback exec of 'true' failed: %v", err)
	}
}

// TestFallbackExec_EchoIntegration verifies echo works through fallback exec.
func TestFallbackExec_EchoIntegration(t *testing.T) {
	cmd := fallbackExec([]string{"echo", "hello"})
	out, err := cmd.Output()
	if err != nil {
		applogger.Error("sandbox test: fallback exec of echo failed", "error", err)
		t.Fatalf("fallback exec of echo failed: %v", err)
	}
	if !strings.Contains(string(out), "hello") {
		t.Errorf("expected output to contain 'hello', got %q", string(out))
	}
}

// TestRun_UnsupportedPlatform verifies that Run with an unsupported GOOS
// (not darwin/linux/windows) falls back cleanly.
func TestRun_UnsupportedPlatform(t *testing.T) {
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		t.Skipf("skipping unsupported-platform test on %s (platform is supported)", runtime.GOOS)
	default:
		cmd, _, err := Run("/tmp/ws", 1, 1, []string{"true"})
		if err != nil {
			applogger.Error("sandbox test: Run failed on unsupported platform", "error", err)
			t.Fatalf("Run() returned error on unsupported platform: %v", err)
		}
		if cmd == nil {
			t.Fatal("expected non-nil *exec.Cmd on unsupported platform")
		}
	}
}

// TestRun_ArgsPreserved verifies that cmd arguments are not mangled in unknown platforms.
func TestRun_ArgsPreserved(t *testing.T) {
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		t.Skipf("skipping args-preserved test on %s (platform-specific wrapping applies)", runtime.GOOS)
	default:
		cmd, _, err := Run("/tmp/ws", 1, 1, []string{"sh", "-c", "echo test"})
		if err != nil {
			applogger.Error("sandbox test: Run failed on args preserved test", "error", err)
			t.Fatalf("Run() returned error: %v", err)
		}
		if len(cmd.Args) < 3 {
			t.Errorf("expected at least 3 args, got %d: %v", len(cmd.Args), cmd.Args)
		}
	}
}
