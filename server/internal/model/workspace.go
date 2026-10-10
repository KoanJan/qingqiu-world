package model

import "time"

// Workspace is an agent-owned, durable file environment independent of a Session.
// RelativePath is interpreted below that person's Agent Owned Space root.
type Workspace struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID     int64     `gorm:"not null;uniqueIndex:idx_workspace_owner_path;index:idx_workspace_owner_created" json:"person_id"`
	RelativePath string    `gorm:"type:text;not null;uniqueIndex:idx_workspace_owner_path" json:"relative_path"`
	Name         string    `gorm:"type:text;not null" json:"name"`
	Purpose      string    `gorm:"type:text;not null;default:''" json:"purpose"`
	CreatedAt    time.Time `gorm:"not null;autoCreateTime;index:idx_workspace_owner_created" json:"created_at"`
}

// TableName returns the durable Workspace table name.
func (Workspace) TableName() string { return "workspaces" }

// WorkspaceUseSource identifies the activity that explicitly used a Workspace.
type WorkspaceUseSource int

const (
	WorkspaceUseWork WorkspaceUseSource = iota + 1
	WorkspaceUsePrivateSpaceAction
)

// WorkspaceUseRole distinguishes the selected default from explicit extra use.
type WorkspaceUseRole int

const (
	WorkspaceUseDefault WorkspaceUseRole = iota + 1
	WorkspaceUseExplicit
)

// WorkspaceUse records a declared activity-to-Workspace association, not file access.
type WorkspaceUse struct {
	ID          int64              `gorm:"primaryKey;autoIncrement" json:"id"`
	WorkspaceID int64              `gorm:"not null;index:idx_workspace_use_workspace;uniqueIndex:idx_workspace_use_source" json:"workspace_id"`
	SourceType  WorkspaceUseSource `gorm:"not null;uniqueIndex:idx_workspace_use_source" json:"source_type"`
	SourceID    int64              `gorm:"not null;uniqueIndex:idx_workspace_use_source" json:"source_id"`
	Role        WorkspaceUseRole   `gorm:"not null;uniqueIndex:idx_workspace_use_source" json:"role"`
	CreatedAt   time.Time          `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName returns the declared use table name.
func (WorkspaceUse) TableName() string { return "workspace_uses" }
