package sandbox

import (
	"fmt"
	"os/exec"

	applogger "qingqiu-world-server/internal/logger"
)

// runWindows executes the command via plain os/exec without sandbox isolation.
//
// Windows AppContainer sandbox is not implemented. Reasons:
//   - Requires low-level Win32 P/Invoke (CreateAppContainerProfile,
//     ConvertStringSidToSid, InitializeProcThreadAttributeList,
//     SetNamedSecurityInfo, CreateProcess with EXTENDED_STARTUPINFO_PRESENT)
//     that are not provided by golang.org/x/sys/windows and must be
//     hand-written (~200-300 lines of syscall structs and constants).
//   - No Windows development or test environment is currently available.
//
// The full AppContainer implementation design is documented in
// draft/sandbox/pbsandbox.md section 3.3.
func runWindows(personID, sessionID int64, cmd []string) (*exec.Cmd, bool, error) {
	applogger.Error("sandbox: Windows AppContainer not implemented, falling back to plain exec",
		"person_id", personID, "session_id", sessionID)

	if len(cmd) == 0 {
		return nil, false, fmt.Errorf("sandbox: cmd is empty")
	}
	return exec.Command(cmd[0], cmd[1:]...), false, nil
}
