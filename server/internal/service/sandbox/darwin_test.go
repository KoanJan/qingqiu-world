package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	applogger "qingqiu-world-server/internal/logger"
	"strings"
	"testing"
)

func requireDarwin(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sw_vers"); err != nil {
		applogger.Error("sandbox: sw_vers lookup failed, skipping darwin test", "error", err)
		t.Skip("test requires macOS")
	}
}

// sandboxExecRunnable returns true if sandbox-exec can apply a Seatbelt policy
// in the current environment. Uses a minimal valid policy for accurate detection:
// - "no version specified" (exit 65) means the policy file is invalid, not that the sandbox is unavailable
// - "sandbox_apply: Operation not permitted" (exit 71) means SIP blocked sandbox_apply
func sandboxExecRunnable() bool {
	// minimal valid Seatbelt policy — without (version 1) the error is "no version specified"
	const minimalPolicy = "(version 1)\n(allow default)\n"
	f, err := os.CreateTemp("", "pbsb-check-*.sb")
	if err != nil {
		return false
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(minimalPolicy); err != nil {
		f.Close()
		return false
	}
	f.Close()
	_, err = exec.Command("/usr/bin/sandbox-exec", "-f", f.Name(), "--", "true").CombinedOutput()
	return err == nil
}

// TestGeneratePolicy_PlaceholdersReplaced verifies that the generated Seatbelt policy replaces
// the $WORKSPACE and $DATAROOT placeholders with the actual paths.
func TestGeneratePolicy_PlaceholdersReplaced(t *testing.T) {
	requireDarwin(t)

	workspace := "/tmp/test-workspace"
	dataRoot := "/tmp/test-data"
	policy := generatePolicy(workspace, dataRoot)

	if strings.Contains(policy, "$WORKSPACE") {
		t.Error("policy still contains $WORKSPACE placeholder after generation")
	}
	if !strings.Contains(policy, workspace) {
		t.Errorf("policy does not contain workspace path %q", workspace)
	}
	if strings.Contains(policy, "$DATAROOT") {
		t.Error("policy still contains $DATAROOT placeholder after generation")
	}
	if !strings.Contains(policy, dataRoot) {
		t.Errorf("policy does not contain data root path %q", dataRoot)
	}
}

// TestRunDarwin_Echo verifies that sandbox-exec successfully runs a simple echo command
// under a Seatbelt sandbox policy.
func TestRunDarwin_Echo(t *testing.T) {
	requireDarwin(t)
	if !sandboxExecRunnable() {
		t.Skip("sandbox-exec cannot apply policies in this environment (sandbox_apply denied)")
	}

	ws, err := os.MkdirTemp("", "pbtest-darwin-*")
	if err != nil {
		applogger.Error("sandbox test: failed to create temp workspace", "error", err)
		t.Fatalf("failed to create temp workspace: %v", err)
	}
	defer os.RemoveAll(ws)

	execCmd, _, err := runDarwin(ws, ws, []string{"echo", "hello"})
	if err != nil {
		applogger.Error("sandbox test: runDarwin echo failed", "error", err)
		t.Fatalf("runDarwin() returned error: %v", err)
	}

	out, err := execCmd.Output()
	if err != nil {
		applogger.Error("sandbox test: sandbox-exec echo failed", "error", err)
		t.Fatalf("sandbox-exec of echo failed: %v", err)
	}
	if !strings.Contains(string(out), "hello") {
		t.Errorf("expected output to contain 'hello', got %q", string(out))
	}
}

// TestRunDarwin_WriteDenied verifies that the Seatbelt sandbox policy blocks writes
// to protected system directories.
func TestRunDarwin_WriteDenied(t *testing.T) {
	requireDarwin(t)
	if !sandboxExecRunnable() {
		t.Skip("sandbox-exec cannot apply policies in this environment (sandbox_apply denied)")
	}

	ws, err := os.MkdirTemp("", "pbtest-darwin-*")
	if err != nil {
		applogger.Error("sandbox test: failed to create temp workspace", "error", err)
		t.Fatalf("failed to create temp workspace: %v", err)
	}
	defer os.RemoveAll(ws)

	// Attempt to write to a protected directory
	execCmd, _, err := runDarwin(ws, ws, []string{"touch", "/private/etc/sandbox_test_probe"})
	if err != nil {
		applogger.Error("sandbox test: runDarwin write denied test failed", "error", err)
		t.Fatalf("runDarwin() returned error: %v", err)
	}

	out, err := execCmd.CombinedOutput()
	if err == nil {
		// Success means write was NOT blocked — sandbox not active
		os.Remove("/private/etc/sandbox_test_probe")
		t.Error("write to /private/etc should have been denied by Seatbelt sandbox")
		return
	}
	combined := string(out)
	if !strings.Contains(combined, "deny") && !strings.Contains(combined, "Operation not permitted") {
		t.Errorf("expected Seatbelt deny message in output, got: %s", combined)
	}
}

// TestRunDarwin_WriteAllowed verifies that the Seatbelt sandbox policy allows writes
// within the designated workspace directory.
func TestRunDarwin_WriteAllowed(t *testing.T) {
	requireDarwin(t)
	if !sandboxExecRunnable() {
		t.Skip("sandbox-exec cannot apply policies in this environment (sandbox_apply denied)")
	}

	ws, err := os.MkdirTemp("", "pbtest-darwin-*")
	if err != nil {
		applogger.Error("sandbox test: failed to create temp workspace", "error", err)
		t.Fatalf("failed to create temp workspace: %v", err)
	}
	defer os.RemoveAll(ws)

	testFile := filepath.Join(ws, "sandbox_write_test.txt")
	execCmd, _, err := runDarwin(ws, ws, []string{"touch", testFile})
	if err != nil {
		applogger.Error("sandbox test: runDarwin write allowed test failed", "error", err)
		t.Fatalf("runDarwin() returned error: %v", err)
	}

	if out, err := execCmd.CombinedOutput(); err != nil {
		applogger.Error("sandbox test: touch workspace file failed", "error", err)
		t.Fatalf("touch workspace file failed: %v\n%s", err, string(out))
	}

	if _, statErr := os.Stat(testFile); os.IsNotExist(statErr) {
		t.Error("workspace file was not created — sandbox may be blocking legitimate writes")
	}
}

// TestRunDarwin_EmptyCmd verifies that Run returns an error when given an empty
// command slice on macOS.
func TestRunDarwin_EmptyCmd(t *testing.T) {
	requireDarwin(t)

	_, _, err := Run("/tmp/ws", "/tmp/ws", []string{})
	if err == nil {
		t.Error("expected error for empty cmd")
	}
}

// runSandboxExec applies the given Seatbelt policy file to a command and returns
// the error from sandbox-exec. A nil error means the command ran successfully
// under the policy; a non-nil error means the policy blocked it (or exec failed).
func runSandboxExec(policyPath string, args ...string) error {
	cmdArgs := append([]string{"-f", policyPath}, args...)
	_, err := exec.Command("/usr/bin/sandbox-exec", cmdArgs...).CombinedOutput()
	return err
}

// TestSeatbeltDataConfinement verifies the data/ confinement rule: the agent's own
// AOS subdir is readable and writable, while every other subtree under data/ is
// denied (invisible and unwritable).
func TestSeatbeltDataConfinement(t *testing.T) {
	requireDarwin(t)
	if !sandboxExecRunnable() {
		t.Skip("sandbox-exec cannot apply policies (sandbox_apply denied)")
	}

	dataRoot := t.TempDir()
	workspace := filepath.Join(dataRoot, "aos", "2")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dataRoot, "aosmeta"), 0700); err != nil {
		t.Fatalf("failed to create aosmeta: %v", err)
	}

	// Canonicalize to match the symlink-resolved paths Seatbelt actually evaluates
	// (macOS exposes /var -> /private/var, /tmp -> /private/tmp).
	dataRoot = absolutePath(dataRoot)
	workspace = absolutePath(workspace)

	// Pre-create files used for the read checks.
	if err := os.WriteFile(filepath.Join(workspace, "ok.txt"), []byte("hello"), 0600); err != nil {
		t.Fatalf("failed to write workspace file: %v", err)
	}
	secret := filepath.Join(dataRoot, "aosmeta", "secret.txt")
	if err := os.WriteFile(secret, []byte("topsecret"), 0600); err != nil {
		t.Fatalf("failed to write secret file: %v", err)
	}

	policy := generatePolicy(workspace, dataRoot)
	policyFile, err := os.CreateTemp("", "pbsb-confinement-*.sb")
	if err != nil {
		t.Fatalf("failed to create policy file: %v", err)
	}
	defer os.Remove(policyFile.Name())
	if _, err := policyFile.WriteString(policy); err != nil {
		policyFile.Close()
		t.Fatalf("failed to write policy: %v", err)
	}
	policyFile.Close()

	// Read inside own AOS must be allowed.
	if err := runSandboxExec(policyFile.Name(), "cat", filepath.Join(workspace, "ok.txt")); err != nil {
		t.Errorf("read inside own AOS should succeed, got: %v", err)
	}
	// Write inside own AOS must be allowed.
	if err := runSandboxExec(policyFile.Name(), "touch", filepath.Join(workspace, "new.txt")); err != nil {
		t.Errorf("write inside own AOS should succeed, got: %v", err)
	}
	// Read a sibling subtree under data/ must be denied.
	if err := runSandboxExec(policyFile.Name(), "cat", secret); err == nil {
		t.Error("read of data/aosmeta/secret.txt should have been denied")
	}
	// Write a sibling subtree under data/ must be denied.
	if err := runSandboxExec(policyFile.Name(), "touch", filepath.Join(dataRoot, "aosmeta", "evil.txt")); err == nil {
		t.Error("write to data/aosmeta/evil.txt should have been denied")
	}
}
