// Package sandbox provides cross-platform kernel-level sandbox execution for agent commands.
//
// The single external entry point is:
//
//	Run(workspace, policyDir string, cmd []string) (*exec.Cmd, bool, error)
//
// Internally dispatches to platform-native mechanisms:
//   - macOS: sandbox-exec (Seatbelt MACF)
//   - Linux: bubblewrap (user namespaces + mount namespaces)
//   - Windows: plain exec (AppContainer not yet implemented)
//
// Design principle: availability over security. When the platform sandbox is unavailable,
// fall back to plain os/exec without blocking focused work.
//
// The returned *exec.Cmd is managed by the caller (BashTool) — stdout/stderr collection,
// truncation, and timeout kill remain unchanged.
package sandbox

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"

	applogger "qingqiu-world-server/internal/logger"
)

// Run executes a command within the platform sandbox. If the sandbox mechanism
// is unavailable, falls back to plain os/exec.
//
// Returns the exec.Cmd, a sandboxed flag (true if the command is wrapped in a
// platform sandbox, false for plain exec fallback), and any error.
//
// workspace is the writable area path.
// policyDir is the directory for sandbox policy files (macOS Seatbelt policy
// is stored as {policyDir}/sandbox.sb). Use empty string when not needed.
// cmd is the command and its arguments.
func Run(workspace, policyDir string, cmd []string) (*exec.Cmd, bool, error) {
	if len(cmd) == 0 {
		return nil, false, fmt.Errorf("sandbox: cmd is empty")
	}

	switch runtime.GOOS {
	case "darwin":
		return runDarwin(workspace, policyDir, cmd)
	case "linux":
		return runLinux(workspace, cmd)
	case "windows":
		return runWindows(policyDir, cmd)
	default:
		applogger.Error("sandbox: unsupported platform, plain exec",
			"goos", runtime.GOOS)
		return exec.Command(cmd[0], cmd[1:]...), false, nil
	}
}

// absolutePath resolves a filesystem path to its canonical absolute form by
// following symlinks. Seatbelt subpath filters and bubblewrap mounts match
// against the symlink-resolved ("real") path, so a symlinked component (e.g.
// macOS /var -> /private/var, /tmp -> /private/tmp) must be resolved or the
// filters silently fail to match. Falls back to the non-resolved absolute form
// (logging the fallback) when resolution is impossible.
func absolutePath(path string) string {
	abs, absErr := filepath.Abs(path)
	if absErr != nil {
		applogger.Error("sandbox: failed to make path absolute", "path", path, "error", absErr)
		abs = path
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		applogger.Error("sandbox: failed to resolve symlinks for path, using absolute form",
			"path", abs, "error", err)
		return abs
	}
	return real
}
