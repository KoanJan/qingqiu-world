package model

import "time"

// FocusSource identifies the runtime loop that produced a handoff.
type FocusSource int

// Focus source constants. Int values keep persisted enum semantics stable.
const (
	FocusSourceExternal FocusSource = iota
	FocusSourcePrivate
)

// FocusHandoffStatus identifies the terminal or paused outcome in a handoff.
type FocusHandoffStatus int

// Focus handoff status constants. A handoff is immutable once persisted.
const (
	FocusHandoffCompleted FocusHandoffStatus = iota
	FocusHandoffFailed
	FocusHandoffCancelled
	FocusHandoffInterrupted
	FocusHandoffPaused
)

// FocusHandoff is runtime-owned, compact continuity metadata. WorkID is zero
// for private-loop handoffs; SessionID is zero for non-session private work.
// No database foreign keys are used because lifecycle cleanup is application-owned.
type FocusHandoff struct {
	ID                 int64              `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID           int64              `gorm:"not null;index:idx_focus_handoff_person_session" json:"person_id"`
	SessionID          int64              `gorm:"not null;index:idx_focus_handoff_person_session" json:"session_id"`
	WorkID             int64              `gorm:"not null;index" json:"work_id"`
	Source             FocusSource        `gorm:"not null" json:"source"`
	Status             FocusHandoffStatus `gorm:"not null" json:"status"`
	Orientation        string             `gorm:"type:text;not null" json:"orientation"`
	Summary            string             `gorm:"type:text;not null" json:"summary"`
	ConfirmedFindings  string             `gorm:"type:text;not null;default:''" json:"confirmed_findings"`
	ArtifactReferences string             `gorm:"type:text;not null;default:''" json:"artifact_references"`
	Unresolved         string             `gorm:"type:text;not null" json:"unresolved"`
	NextStep           string             `gorm:"type:text;not null" json:"next_step"`
	CreatedAt          time.Time          `gorm:"not null;autoCreateTime;index" json:"created_at"`
}

// TableName returns the database table name for FocusHandoff.
func (FocusHandoff) TableName() string { return "focus_handoffs" }
