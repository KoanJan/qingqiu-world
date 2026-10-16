package model

import "time"

// MemoStatus distinguishes a current reminder from one retained for recall.
type MemoStatus int

const (
	MemoStatusActive MemoStatus = iota + 1
	MemoStatusArchived
)

// MemoOperation is the change requested by one top-level Action.
type MemoOperation int

const (
	MemoOperationCreate MemoOperation = iota + 1
	MemoOperationRevise
	MemoOperationSetPinned
	MemoOperationArchive
	MemoOperationRestore
)

// Memo is an agent-owned, mutable note to its future self. The ActionEffect
// records which accepted Action changed it; Content is not a provenance field.
type Memo struct {
	// ID is the stable resource reference used by later changes and recall.
	ID int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// PersonID is the sole owner; Session and Work do not own this resource.
	PersonID int64 `gorm:"not null;index:idx_memos_owner_status_pinned_updated,priority:1" json:"person_id"`
	// Content is the complete current note, not its causal explanation.
	Content string `gorm:"type:text;not null" json:"content"`
	// Status controls everyday visibility without deleting the note.
	Status MemoStatus `gorm:"not null;index:idx_memos_owner_status_pinned_updated,priority:2" json:"status"`
	// Pinned is the owner's choice to include a preview in Subject.
	Pinned bool `gorm:"not null;default:false;index:idx_memos_owner_status_pinned_updated,priority:3" json:"pinned"`
	// Revision prevents an older Action from overwriting a newer change.
	Revision int64 `gorm:"not null" json:"revision"`
	// CreatedAt and UpdatedAt describe the resource lifecycle, not observation.
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null;autoUpdateTime;index:idx_memos_owner_status_pinned_updated,priority:4" json:"updated_at"`
}

// TableName returns the durable table for current memo state.
func (Memo) TableName() string { return "memos" }
