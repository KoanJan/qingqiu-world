package sandbox

import (
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"qingqiu-world-server/internal/config"

	applogger "qingqiu-world-server/internal/logger"
)

//go:embed seatbelt_template.sb
var seatbeltTemplate string

// darwinSandboxAvailable caches the result of the one-time sandbox-exec capability check.
// On macOS with SIP enabled, sandbox_apply is blocked — sandbox-exec cannot load custom
// Seatbelt policies. In that case all commands fall back to plain exec.
var (
	darwinSandboxAvailable   bool
	darwinSandboxCheckedOnce sync.Once
)

// checkDarwinSandbox tests whether sandbox-exec can apply a Seatbelt policy
// in the current environment. Result is cached after the first call.
func checkDarwinSandbox() bool {
	darwinSandboxCheckedOnce.Do(func() {
		const probePolicy = "(version 1)\n(allow default)\n"
		f, err := os.CreateTemp("", "pbsb-check-*.sb")
		if err != nil {
			applogger.Error("sandbox: cannot create temp policy for probe, Seatbelt unavailable", "error", err)
			return
		}
		defer os.Remove(f.Name())
		if _, err := f.WriteString(probePolicy); err != nil {
			f.Close()
			applogger.Error("sandbox: cannot write probe policy, Seatbelt unavailable", "error", err)
			return
		}
		f.Close()
		cmd := exec.Command("/usr/bin/sandbox-exec", "-f", f.Name(), "--", "true")
		if out, err := cmd.CombinedOutput(); err != nil {
			applogger.Error("sandbox: Seatbelt unavailable, falling back to plain exec",
				"error", err, "output", string(out))
			return
		}
		darwinSandboxAvailable = true
		applogger.Info("sandbox: Seatbelt available — macOS sandbox active")
	})
	return darwinSandboxAvailable
}

// seatbeltTemplate is the Seatbelt sandbox policy template for macOS.
// $WORKSPACE is replaced at runtime with the actual session workspace path.
//
// Design principle (availability over security):
//   - file-read* and process-exec are fully allowed (via allow default)
//   - file-write* is restricted to workspace + tmp dirs + /dev nodes
//   - network-outbound and socket operations are fully allowed (via allow default)
//   - mach-lookup is NOT covered by (allow default) — explicit allow rules
//     are required for DNS (mDNSResponder) and network config (configd)

// runDarwin executes the command inside macOS sandbox-exec with a Seatbelt policy.
//
// Policy files are stored in {DATA_ROOT}/aac/{personID}/{sessionID}/sandbox.sb
// (outside the person-writable workspace, preventing tampering). The policy is
// generated once per session and reused for subsequent calls.
func runDarwin(workspace string, personID, sessionID int64, cmd []string) (*exec.Cmd, bool, error) {
	// One-time check: if sandbox-exec cannot apply policies (SIP block, etc.), fall back
	if !checkDarwinSandbox() {
		return fallbackExec(cmd), false, nil
	}

	policyDir := filepath.Join(config.Get().GetDataRoot(), "aac",
		strconv.FormatInt(personID, 10), strconv.FormatInt(sessionID, 10))
	policyPath := filepath.Join(policyDir, "sandbox.sb")

	// Resolve to absolute path: the bash tool sets cmd.Dir to the output
	// directory, so a relative policy path would resolve incorrectly when
	// sandbox-exec runs in that working directory.
	absPolicyPath, err := filepath.Abs(policyPath)
	if err != nil {
		applogger.Error("sandbox: failed to resolve absolute policy path, falling back to plain exec",
			"path", policyPath, "error", err)
		return fallbackExec(cmd), false, nil
	}

	// Generate policy file once per session
	if _, err := os.Stat(absPolicyPath); err != nil {
		if !os.IsNotExist(err) {
			applogger.Error("sandbox: failed to stat policy file, falling back to plain exec",
				"path", absPolicyPath, "error", err)
			return fallbackExec(cmd), false, nil
		}
		if err := os.MkdirAll(policyDir, 0700); err != nil {
			applogger.Error("sandbox: failed to create AAC policy directory, falling back to plain exec",
				"dir", policyDir, "error", err)
			return fallbackExec(cmd), false, nil
		}
		policy := generatePolicy(workspace)
		if err := os.WriteFile(absPolicyPath, []byte(policy), 0600); err != nil {
			applogger.Error("sandbox: failed to write policy file, falling back to plain exec",
				"path", absPolicyPath, "error", err)
			return fallbackExec(cmd), false, nil
		}
		applogger.Info("sandbox: generated Seatbelt policy",
			"path", absPolicyPath, "workspace", workspace)
	}

	sandboxArgs := append([]string{"-f", absPolicyPath}, cmd...)
	applogger.Debug("sandbox: active — Seatbelt (sandbox-exec)")
	return exec.Command("/usr/bin/sandbox-exec", sandboxArgs...), true, nil
}

// generatePolicy replaces the $WORKSPACE placeholder in the Seatbelt template
// with the actual workspace path.
func generatePolicy(workspace string) string {
	return strings.ReplaceAll(seatbeltTemplate, "$WORKSPACE", workspace)
}

// fallbackExec returns a plain exec.Cmd as a fallback when sandbox setup fails.
func fallbackExec(cmd []string) *exec.Cmd {
	if len(cmd) == 0 {
		return exec.Command("true")
	}
	return exec.Command(cmd[0], cmd[1:]...)
}
