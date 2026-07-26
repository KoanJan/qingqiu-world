package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	applogger "qingqiu-world-server/internal/logger"
)

// bwrap path cache — extracted once per process lifetime.
var (
	bwrapPath     string
	bwrapPathOnce sync.Once
	bwrapPathErr  error
)

// bwrapLookup returns the path to the embedded bwrap binary.
// On first call, the binary is extracted from the embedded data to a temp file
// and made executable. Subsequent calls return the cached path.
//
// Returns an error if the embedded binary is not available or extraction fails.
func bwrapLookup() (string, error) {
	bwrapPathOnce.Do(func() {
		if len(bwrapBinary) == 0 {
			bwrapPathErr = fmt.Errorf("bwrap binary not embedded for %s/%s (build bwrap from source for this architecture)", runtime.GOOS, runtime.GOARCH)
			return
		}
		f, err := os.CreateTemp("", "pb-bwrap-*")
		if err != nil {
			bwrapPathErr = fmt.Errorf("create bwrap temp file: %w", err)
			return
		}
		defer f.Close()
		if _, err := f.Write(bwrapBinary); err != nil {
			bwrapPathErr = fmt.Errorf("write bwrap binary: %w", err)
			return
		}
		if err := os.Chmod(f.Name(), 0700); err != nil {
			bwrapPathErr = fmt.Errorf("chmod bwrap: %w", err)
			return
		}
		bwrapPath = f.Name()
		applogger.Info("sandbox: extracted embedded bwrap",
			"path", bwrapPath, "size_bytes", len(bwrapBinary))
	})
	return bwrapPath, bwrapPathErr
}

// BwrapAvailable checks whether bubblewrap is available and can create
// user namespaces. Returns true if the sandbox can be used.
//
// Uses the embedded bwrap binary extracted at runtime. Detection uses a
// 5-second timeout to prevent hanging if bwrap is blocked by security policy.
func BwrapAvailable() (bool, error) {
	path, err := bwrapLookup()
	if err != nil {
		return false, err
	}

	// Directly test namespace creation — the most reliable detection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "--ro-bind", "/", "/", "true")
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("bwrap namespace creation failed: %w (check kernel.unprivileged_userns_clone and apparmor_restrict_unprivileged_userns)", err)
	}
	return true, nil
}

// runLinux executes the command inside a bubblewrap sandbox.
//
// Design: allow-default — entire root filesystem is mounted read-only,
// workspace is read-write, network is shared. Simpler and more robust than
// per-path binding (no need to enumerate /etc files or detect FHS symlinks).
//
//   - Root filesystem: read-only bind
//   - Workspace: read-write bind
//   - /tmp, /var/tmp, /dev/shm: tmpfs (writable, isolated)
//   - Process isolation: new session, PID namespace, IPC namespace, drop all caps
//   - Network: shared with host
//
// Uses the embedded bwrap binary. Falls back to plain exec if bwrap is unavailable.
func runLinux(workspace string, cmd []string) (*exec.Cmd, bool, error) {
	path, err := bwrapLookup()
	if err != nil {
		applogger.Error("sandbox: bwrap unavailable, falling back to plain exec",
			"error", err)
		return fallbackExec(cmd), false, nil
	}

	available, err := BwrapAvailable()
	if err != nil {
		applogger.Error("sandbox: bwrap unavailable, falling back to plain exec",
			"error", err)
		return fallbackExec(cmd), false, nil
	}
	if !available {
		applogger.Error("sandbox: bwrap not available, falling back to plain exec")
		return fallbackExec(cmd), false, nil
	}

	var args []string

	// --- Read-only root filesystem (allow-default for reads) ---
	args = append(args, "--ro-bind", "/", "/")

	// --- Read-write workspace ---
	args = append(args, "--bind", workspace, workspace)

	// --- Basic devices and proc ---
	args = append(args, "--dev", "/dev", "--proc", "/proc")

	// --- Temporary filesystems (writable, isolated from host) ---
	args = append(args,
		"--tmpfs", "/tmp",
		"--tmpfs", "/var/tmp",
		"--tmpfs", "/dev/shm",
	)

	// --- Network: shared with host ---
	args = append(args, "--share-net")

	// --- Process isolation ---
	args = append(args,
		"--new-session",
		"--unshare-pid",
		"--unshare-ipc",
		"--cap-drop", "ALL",
	)

	// --- Command ---
	args = append(args, "--")
	args = append(args, cmd...)

	applogger.Debug("sandbox: active — bubblewrap")
	return exec.Command(path, args...), true, nil
}
