package dops

import (
	"fmt"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// ActivityAgent is the owner identity shown by the Focus activity browser.
type ActivityAgent struct {
	ID            int64              `json:"id"`
	Name          string             `json:"name"`
	Avatar        string             `json:"avatar"`
	Status        model.PersonStatus `json:"status"`
	HasActiveWork bool               `json:"has_active_work"`
}

// ActivityWorkspace is a workspace with its current Focus activity indicator.
type ActivityWorkspace struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Purpose       string `json:"purpose"`
	HasActiveWork bool   `json:"has_active_work"`
}

// ActivityWork is a Focus work associated with the selected workspace.
// DefaultWorkspaceID identifies where its complete Interaction history lives.
type ActivityWork struct {
	ID                   int64                  `json:"id"`
	Description          string                 `json:"description"`
	Status               model.WorkStatus       `json:"status"`
	CreatedAt            time.Time              `json:"created_at"`
	UpdatedAt            time.Time              `json:"updated_at"`
	Role                 model.WorkspaceUseRole `json:"role"`
	DefaultWorkspaceID   int64                  `json:"default_workspace_id"`
	DefaultWorkspaceName string                 `json:"default_workspace_name"`
}

// ListActivityAgents includes deceased agents so their recorded Focus history
// remains browsable after their runtime has stopped.
func ListActivityAgents() ([]ActivityAgent, error) {
	agents := make([]ActivityAgent, 0)
	err := database.DB.Raw(`
SELECT p.id, p.name, p.avatar, p.status,
       EXISTS(SELECT 1 FROM works w WHERE w.person_id = p.id AND w.status = ?) AS has_active_work
FROM persons p
JOIN agent_configs a ON a.person_id = p.id
ORDER BY p.id`, model.WorkStatusRunning).Scan(&agents).Error
	return agents, err
}

// ListActivityWorkspaces pages an agent's workspaces with active ones first.
// The owner predicate is applied before association rows are aggregated.
func ListActivityWorkspaces(personID int64, page, limit int) ([]ActivityWorkspace, bool, error) {
	if page < 1 || limit < 1 || limit > 100 {
		return nil, false, fmt.Errorf("invalid activity workspace page")
	}
	records := make([]ActivityWorkspace, 0)
	err := database.DB.Raw(`
SELECT ws.id, ws.name, ws.purpose,
       COALESCE(MAX(CASE WHEN w.status = ? THEN 1 ELSE 0 END), 0) AS has_active_work
FROM workspaces ws
LEFT JOIN workspace_uses u ON u.workspace_id = ws.id AND u.source_type = ?
LEFT JOIN works w ON w.id = u.source_id AND w.person_id = ws.person_id
WHERE ws.person_id = ?
GROUP BY ws.id
ORDER BY has_active_work DESC, COALESCE(MAX(w.created_at), ws.created_at) DESC, ws.id DESC
LIMIT ? OFFSET ?`, model.WorkStatusRunning, model.WorkspaceUseWork, personID, limit+1, (page-1)*limit).Scan(&records).Error
	if err != nil {
		return nil, false, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	return records, hasMore, nil
}

// ListActivityWorks pages Focus works linked to an owned workspace. A Work
// with an explicit extra use is returned as a reference to its default workspace.
func ListActivityWorks(personID, workspaceID, beforeWorkID int64, limit int) ([]ActivityWork, bool, error) {
	if limit < 1 || limit > 100 {
		return nil, false, fmt.Errorf("invalid activity work limit")
	}
	records := make([]ActivityWork, 0)
	query := database.DB.Table("workspace_uses AS u").
		Select(`w.id, w.description, w.status, w.created_at, w.updated_at, MIN(u.role) AS role,
            COALESCE((SELECT d.workspace_id FROM workspace_uses d JOIN workspaces dw ON dw.id = d.workspace_id AND dw.person_id = w.person_id
		              WHERE d.source_type = ? AND d.source_id = w.id AND d.role = ? LIMIT 1), 0) AS default_workspace_id,
		    COALESCE((SELECT dw.name FROM workspace_uses d JOIN workspaces dw ON dw.id = d.workspace_id AND dw.person_id = w.person_id
		              WHERE d.source_type = ? AND d.source_id = w.id AND d.role = ? LIMIT 1), '') AS default_workspace_name`,
			model.WorkspaceUseWork, model.WorkspaceUseDefault, model.WorkspaceUseWork, model.WorkspaceUseDefault).
		Joins("JOIN workspaces AS ws ON ws.id = u.workspace_id AND ws.person_id = ?", personID).
		Joins("JOIN works AS w ON w.id = u.source_id AND w.person_id = ws.person_id").
		Where("u.workspace_id = ? AND u.source_type = ?", workspaceID, model.WorkspaceUseWork).
		Group("w.id")
	if beforeWorkID > 0 {
		query = query.Where("w.id < ?", beforeWorkID)
	}
	if err := query.Order("w.id DESC").Limit(limit + 1).Scan(&records).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	return records, hasMore, nil
}

// GetOwnedActivityWork enforces ownership before exposing an Interaction page.
func GetOwnedActivityWork(personID, workID int64) (*model.Work, error) {
	var work model.Work
	if err := database.DB.Where("id = ? AND person_id = ?", workID, personID).Take(&work).Error; err != nil {
		return nil, err
	}
	return &work, nil
}
