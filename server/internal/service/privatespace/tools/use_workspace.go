package tools

import (
	"fmt"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
	servicetools "qingqiu-world-server/internal/service/tools"
)

// UseWorkspaceTool records a private-space run's explicit use of an owned Workspace.
type UseWorkspaceTool struct {
	personID        int64
	currentActionID func() int64
}

// NewUseWorkspaceTool binds the tool to the currently running PS start Action.
func NewUseWorkspaceTool(personID int64, currentActionID func() int64) *UseWorkspaceTool {
	return &UseWorkspaceTool{personID: personID, currentActionID: currentActionID}
}

func (t *UseWorkspaceTool) Name() string { return "use_workspace" }
func (t *UseWorkspaceTool) Description() string {
	return "Record that this private-space activity uses an owned Workspace"
}
func (t *UseWorkspaceTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{Name: t.Name(), Description: "Declare that this private-space activity uses a registered Workspace. This records an association and reveals its path; it does not change your default directory or audit every file operation.", Parameters: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"workspace_id": map[string]interface{}{"type": "integer", "description": "Owned Workspace ID"}}, "required": []string{"workspace_id"}}}
}
func (t *UseWorkspaceTool) Execute(args map[string]interface{}) (string, error) {
	id, ok := args["workspace_id"].(float64)
	if !ok || id <= 0 || id != float64(int64(id)) {
		return "", fmt.Errorf("workspace_id must be a positive integer")
	}
	actionID := t.currentActionID()
	if actionID <= 0 {
		return "", fmt.Errorf("private-space run has no initiating Action")
	}
	return servicetools.UseWorkspace(t.personID, int64(id), model.WorkspaceUsePrivateSpaceAction, actionID)
}
