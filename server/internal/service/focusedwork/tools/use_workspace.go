package tools

import (
	"fmt"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
	servicetools "qingqiu-world-server/internal/service/tools"
)

// UseWorkspaceTool records a Focus's explicit use of an additional Workspace.
type UseWorkspaceTool struct {
	personID int64
	workID   int64
	CycleDetector
}

// NewUseWorkspaceTool binds a declared use to one running Work.
func NewUseWorkspaceTool(personID, workID int64) *UseWorkspaceTool {
	return &UseWorkspaceTool{personID: personID, workID: workID}
}

func (t *UseWorkspaceTool) Name() ToolName { return ToolNameUseWorkspace }
func (t *UseWorkspaceTool) Description() string {
	return "Record that this Focus uses another owned Workspace"
}
func (t *UseWorkspaceTool) Schema() llm.FunctionDefinition {
	return llm.FunctionDefinition{Name: t.Name().String(), Description: "Declare that this Work will use another registered Workspace. This records an association and reveals its path; it does not change your default working directory or audit every file operation.", Parameters: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"workspace_id": map[string]interface{}{"type": "integer", "description": "Owned Workspace ID to use"}}, "required": []string{"workspace_id"}}}
}
func (t *UseWorkspaceTool) Execute(args map[string]interface{}) (string, error) {
	id, ok := parseInt64(args["workspace_id"])
	if !ok || id <= 0 {
		return "", fmt.Errorf("workspace_id must be a positive integer")
	}
	return servicetools.UseWorkspace(t.personID, id, model.WorkspaceUseWork, t.workID)
}
