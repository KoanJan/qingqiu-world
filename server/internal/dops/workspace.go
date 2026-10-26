package dops

import (
	"fmt"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"
)

// GetOwnedWorkspace returns a Workspace only when the requesting person owns it.
func GetOwnedWorkspace(personID, workspaceID int64) (*model.Workspace, error) {
	var record model.Workspace
	if err := database.DB.Where("id = ? AND person_id = ?", workspaceID, personID).Take(&record).Error; err != nil {
		return nil, fmt.Errorf("owned workspace %d: %w", workspaceID, err)
	}
	return &record, nil
}

// GetDefaultWorkWorkspace returns the Workspace explicitly selected for Work.
func GetDefaultWorkWorkspace(personID, workID int64) (*model.Workspace, error) {
	var record model.Workspace
	err := database.DB.Table("workspaces AS w").Select("w.*").
		Joins("JOIN workspace_uses AS u ON u.workspace_id = w.id").
		Where("w.person_id = ? AND u.source_type = ? AND u.source_id = ? AND u.role = ?", personID, model.WorkspaceUseWork, workID, model.WorkspaceUseDefault).
		Take(&record).Error
	if err != nil {
		return nil, fmt.Errorf("default workspace for work %d: %w", workID, err)
	}
	return &record, nil
}

// ListRecentWorkspaces returns a bounded owner-only inventory ordered by recency.
func ListRecentWorkspaces(personID int64, limit int) ([]model.Workspace, int64, error) {
	if limit < 1 || limit > 21 {
		limit = 8
	}
	var count int64
	if err := database.DB.Model(&model.Workspace{}).Where("person_id = ?", personID).Count(&count).Error; err != nil {
		return nil, 0, err
	}
	var records []model.Workspace
	err := database.DB.Where("person_id = ?", personID).Order("created_at DESC, id DESC").Limit(limit).Find(&records).Error
	return records, count, err
}

// SearchWorkspaces pages registered metadata; no filesystem content is read.
func SearchWorkspaces(personID, workspaceID int64, query string, from, to time.Time, page, limit int) ([]model.Workspace, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 21 {
		limit = 10
	}
	q := database.DB.Where("person_id = ?", personID)
	if workspaceID > 0 {
		q = q.Where("id = ?", workspaceID)
	}
	if query != "" {
		like := "%" + query + "%"
		q = q.Where("name LIKE ? OR purpose LIKE ?", like, like)
	}
	if !from.IsZero() {
		q = q.Where("created_at >= ?", from)
	}
	if !to.IsZero() {
		q = q.Where("created_at <= ?", to)
	}
	var records []model.Workspace
	err := q.Order("created_at DESC, id DESC").Offset((page - 1) * limit).Limit(limit).Find(&records).Error
	return records, err
}

// ListWorkspaceUses pages declared activity associations for one owned Workspace.
func ListWorkspaceUses(personID, workspaceID int64, page, limit int) ([]model.WorkspaceUse, error) {
	if _, err := GetOwnedWorkspace(personID, workspaceID); err != nil {
		return nil, err
	}
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 20 {
		limit = 10
	}
	var uses []model.WorkspaceUse
	err := database.DB.Where("workspace_id = ?", workspaceID).Order("created_at DESC, id DESC").Offset((page - 1) * limit).Limit(limit).Find(&uses).Error
	return uses, err
}

// DeclareWorkspaceUse records an explicit association after checking both
// Workspace ownership and activity ownership. Repeated calls are idempotent.
func DeclareWorkspaceUse(personID, workspaceID int64, source model.WorkspaceUseSource, sourceID int64) error {
	if _, err := GetOwnedWorkspace(personID, workspaceID); err != nil {
		return err
	}
	if sourceID <= 0 {
		return fmt.Errorf("Workspace use has no activity ID")
	}
	switch source {
	case model.WorkspaceUseWork:
		var work model.Work
		if err := database.DB.Where("id = ? AND person_id = ? AND status = ?", sourceID, personID, model.WorkStatusRunning).Take(&work).Error; err != nil {
			return fmt.Errorf("Work cannot declare Workspace use: %w", err)
		}
	case model.WorkspaceUsePrivateSpaceAction:
		var action model.Action
		if err := database.DB.Table("actions AS a").Select("a.*").Joins("JOIN decisions AS d ON d.id = a.decision_id").Where("a.id = ? AND a.type = ? AND d.person_id = ?", sourceID, model.ActionTypeEnterPrivateSpace, personID).Take(&action).Error; err != nil {
			return fmt.Errorf("private-space Action cannot declare Workspace use: %w", err)
		}
	default:
		return fmt.Errorf("unsupported Workspace use source %d", source)
	}
	use := model.WorkspaceUse{WorkspaceID: workspaceID, SourceType: source, SourceID: sourceID, Role: model.WorkspaceUseExplicit}
	return database.DB.Where("workspace_id = ? AND source_type = ? AND source_id = ? AND role = ?", workspaceID, source, sourceID, model.WorkspaceUseExplicit).FirstOrCreate(&use).Error
}
