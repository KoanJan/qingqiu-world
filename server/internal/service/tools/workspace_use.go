package tools

import (
	"fmt"

	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/aos"
)

// UseWorkspace validates a registered owner-owned directory and records an
// explicit declared use. It does not infer use from unrestricted file access.
func UseWorkspace(personID, workspaceID int64, source model.WorkspaceUseSource, sourceID int64) (string, error) {
	record, err := dops.GetOwnedWorkspace(personID, workspaceID)
	if err != nil {
		return "", err
	}
	path, err := aos.ResolveRegisteredPath(*record)
	if err != nil {
		return "", err
	}
	if err := dops.DeclareWorkspaceUse(personID, workspaceID, source, sourceID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Workspace #%d (%s) is available at %s and its use is recorded.", record.ID, record.Name, path), nil
}
