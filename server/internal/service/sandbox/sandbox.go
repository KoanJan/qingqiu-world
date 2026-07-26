// Package sandbox provides cross-platform kernel-level sandbox execution for agent commands.
//
// The single external entry point is:
//
//	Run(workspace string, personID, sessionID int64, cmd []string) (*exec.Cmd, bool, error)
//
// Internally dispatches to platform-native mechanisms:
//   - macOS: sandbox-exec (Seatbelt MACF)
//   - Linux: bubblewrap (user namespaces + mount namespaces)
//   - Windows: plain exec (AppContainer not yet implemented)
//
// Design principle: availability over security. When the platform sandbox is unavailable,
// fall back to plain os/exec without blocking the task.
//
// The returned *exec.Cmd is managed by the caller (BashTool) — stdout/stderr collection,
// truncation, and timeout kill remain unchanged.
package sandbox

import (
	"fmt"
	"os/exec"
	"runtime"

	applogger "qingqiu-world-server/internal/logger"
)

// Run executes a command within the platform sandbox. If the sandbox mechanism
// is unavailable, falls back to plain os/exec.
//
// Returns the exec.Cmd, a sandboxed flag (true if the command is wrapped in a
// platform sandbox, false for plain exec fallback), and any error.
//
// workspace is the session workspace absolute path, used as the writable area.
// personID and sessionID are used for policy file naming (macOS AAC directory,
// Windows AppContainer profile name).
// cmd is the command and its arguments.
func Run(workspace string, personID, sessionID int64, cmd []string) (*exec.Cmd, bool, error) {
	if len(cmd) == 0 {
		return nil, false, fmt.Errorf("sandbox: cmd is empty")
	}

	switch runtime.GOOS {
	case "darwin":
		return runDarwin(workspace, personID, sessionID, cmd)
	case "linux":
		return runLinux(workspace, cmd)
	case "windows":
		return runWindows(personID, sessionID, cmd)
	default:
		applogger.Error("sandbox: unsupported platform, plain exec",
			"goos", runtime.GOOS)
		return exec.Command(cmd[0], cmd[1:]...), false, nil
	}
}
