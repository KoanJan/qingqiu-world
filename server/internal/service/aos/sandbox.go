package aos

import (
	"path/filepath"
	"strconv"

	"qingqiu-world-server/internal/config"
)

// GetWorkSandboxPolicyDir retains the per-person policy directory layout while
// identifying a Focus policy by Work rather than by its originating Session.
func GetWorkSandboxPolicyDir(personID, workID int64) string {
	return filepath.Join(config.Get().GetDataRoot(), "aac",
		strconv.FormatInt(personID, 10), strconv.FormatInt(workID, 10))
}

// GetPrivateSandboxPolicyDir keeps private-loop sandbox policy files in their
// existing per-agent private branch outside agent-writable resources.
func GetPrivateSandboxPolicyDir(personID int64) string {
	return filepath.Join(config.Get().GetDataRoot(), "aac", strconv.FormatInt(personID, 10), "private")
}
