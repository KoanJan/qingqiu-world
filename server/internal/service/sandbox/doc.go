// Package sandbox provides cross-platform kernel-level sandbox execution for agent commands.
//
// The single external entry point is Run(workspace, policyDir, cmd).
// Internally dispatches to platform-native mechanisms:
//   - macOS: sandbox-exec (Seatbelt MACF), with a per-session policy file
//   - Linux: bubblewrap (user namespaces + mount namespaces), embedded bwrap binary
//   - Windows: plain exec (AppContainer planned but not yet implemented)
//
// # Design principle: availability over security
//
// When the platform sandbox is unavailable, commands fall back to plain os/exec
// rather than blocking focused work. This trade-off is deliberate: an agent that
// cannot execute commands is useless, while a sandbox breach (even if unlikely)
// is limited by the fact that agents operate on isolated session workspaces.
//
// # Platform details
//
// macOS (darwin.go):
//   - A one-time probe tests whether sandbox-exec can load Seatbelt policies
//     (SIP may block sandbox_apply). Result is cached via sync.Once.
//   - Policy template (seatbelt_template.sb) hides all data/ subtrees except
//     the current agent's AOS. Outside data/, read, execution and network
//     remain available by default, while protected system directories stay
//     write-denied.
//   - Policy files are stored outside the workspace ({DATA_ROOT}/aac/{personID}/{sessionID}/sandbox.sb)
//     to prevent tampering. Generated once per session, reused for subsequent calls.
//
// Linux (linux.go):
//   - The bwrap binary is embedded per-architecture (bwrap_linux_{amd64,arm64,arm,386}).
//     It is extracted to a temp file at runtime, cached for the process lifetime.
//   - Sandbox strategy: the root filesystem is read-only; data/ is hidden by a
//     tmpfs and the current agent's AOS is re-bound read-write. This avoids
//     enumerating host paths while keeping runtime data inaccessible.
//   - Process isolation: new session, PID namespace, IPC namespace, drop all caps.
//     Network is shared with host (/tmp, /var/tmp, /dev/shm are tmpfs).
//   - BwrapAvailable() tests namespace creation with a 5-second timeout.
//
// Windows (windows.go):
//   - Falls back to plain exec. AppContainer requires ~200-300 lines of Win32
//     syscall bindings (CreateAppContainerProfile, InitializeProcThreadAttributeList,
//     etc.) that are not yet implemented.
//
// # Fallback behavior
//
// Every error path in platform-specific code calls fallbackExec(), which returns a
// plain *exec.Cmd with sandboxed=false. The caller (BashTool) uses sandboxed=false
// to report a warning to the agent but does not block execution.
//
// The returned *exec.Cmd is managed entirely by the caller — stdout/stderr collection,
// truncation, and timeout kill remain unchanged regardless of sandbox status.
package sandbox
