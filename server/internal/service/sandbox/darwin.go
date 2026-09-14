package sandbox

import (
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
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
// $WORKSPACE and $DATAROOT are replaced at runtime with canonical paths.
//
// Design principle (availability over security):
//   - access under data/ is limited to the current agent's AOS
//   - outside data/, reads and process execution are allowed by default
//   - writes to protected system directories remain denied
//   - network-outbound and socket operations are fully allowed (via allow default)
//   - mach-lookup is NOT covered by (allow default) — explicit allow rules
//     are required for DNS (mDNSResponder) and network config (configd)

// runDarwin executes the command inside macOS sandbox-exec with a Seatbelt policy.
//
// policyDir is the directory where the Seatbelt policy file is stored
// ({policyDir}/sandbox.sb). It should be outside the agent-writable workspace
// to prevent tampering.
func runDarwin(workspace, policyDir string, cmd []string) (*exec.Cmd, bool, error) {
	if !checkDarwinSandbox() {
		return fallbackExec(cmd), false, nil
	}

	if policyDir == "" {
		applogger.Error("sandbox: policyDir is empty, falling back to plain exec")
		return fallbackExec(cmd), false, nil
	}

	policyPath := filepath.Join(policyDir, "sandbox.sb")

	absPolicyPath, err := filepath.Abs(policyPath)
	if err != nil {
		applogger.Error("sandbox: failed to resolve absolute policy path, falling back to plain exec",
			"path", policyPath, "error", err)
		return fallbackExec(cmd), false, nil
	}

	policy := generatePolicy(absolutePath(workspace), absolutePath(config.Get().GetDataRoot()))
	existingPolicy, readErr := os.ReadFile(absPolicyPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		applogger.Error("sandbox: failed to read policy file, falling back to plain exec",
			"path", absPolicyPath, "error", readErr)
		return fallbackExec(cmd), false, nil
	}
	if readErr != nil || string(existingPolicy) != policy {
		if err := os.MkdirAll(policyDir, 0700); err != nil {
			applogger.Error("sandbox: failed to create policy directory, falling back to plain exec",
				"dir", policyDir, "error", err)
			return fallbackExec(cmd), false, nil
		}
		if err := os.WriteFile(absPolicyPath, []byte(policy), 0600); err != nil {
			applogger.Error("sandbox: failed to write policy file, falling back to plain exec",
				"path", absPolicyPath, "error", err)
			return fallbackExec(cmd), false, nil
		}
		applogger.Info("sandbox: generated or updated Seatbelt policy",
			"path", absPolicyPath, "workspace", workspace)
	}

	sandboxArgs := append([]string{"-f", absPolicyPath}, cmd...)
	applogger.Debug("sandbox: active — Seatbelt (sandbox-exec)")
	return exec.Command("/usr/bin/sandbox-exec", sandboxArgs...), true, nil
}

// generatePolicy replaces the $WORKSPACE and $DATAROOT placeholders in the
// Seatbelt template with the actual paths. The policy confines access under the
// data root to the agent's own Agent Owned Space root ($WORKSPACE), keeping all
// other data/ subtrees invisible and unwritable. Everything outside data/ keeps
// the (allow default) behavior.
func generatePolicy(workspace, dataRoot string) string {
	replacer := strings.NewReplacer(
		"$WORKSPACE", workspace,
		"$DATAROOT", dataRoot,
	)
	return replacer.Replace(seatbeltTemplate)
}

// fallbackExec returns a plain exec.Cmd as a fallback when sandbox setup fails.
func fallbackExec(cmd []string) *exec.Cmd {
	if len(cmd) == 0 {
		return exec.Command("true")
	}
	return exec.Command(cmd[0], cmd[1:]...)
}
