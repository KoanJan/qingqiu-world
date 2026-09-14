package model

import "time"

// WorkStatus identifies the persisted lifecycle state of a Work.
type WorkStatus int

// Work status constants.
const (
	WorkStatusRunning   WorkStatus = 0  // Work is currently executing
	WorkStatusCompleted WorkStatus = 1  // Work finished successfully
	WorkStatusFailed    WorkStatus = 2  // Work finished but focused work reported failure
	WorkStatusAbandoned WorkStatus = -1 // Work was abandoned (e.g., user correction, cancellation)
)

// Focus phase constants provide a compact runtime checkpoint on the existing
// Work lifecycle record without adding another persistent entity.
type FocusPhase int

const (
	FocusPhaseExecuting FocusPhase = iota
	FocusPhasePaused
	FocusPhaseCompleted
	FocusPhaseFailed
	FocusPhaseCancelled
)

// Work represents a unit of work for an agent within a session.
//
// A Work is created when an agent decides to act on an event, and it may
// absorb subsequent events (e.g., user corrections) during its execution.
// Since 0.1.4, only focused work (FocusedLoop) remains; ChatWork is retired.
//
// Three-layer model: Agent (long-lived entity) → Work (coherent goal) → Iteration (atomic ReAct step)
type Work struct {
	ID          int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID    int64      `gorm:"not null;index:idx_person_status" json:"person_id"`
	SessionID   int64      `gorm:"not null" json:"session_id"`
	Description string     `gorm:"type:text;not null" json:"description"`                   // Natural language description for semantic routing and recovery
	Status      WorkStatus `gorm:"not null;default:0;index:idx_agent_status" json:"status"` // 0=running, 1=completed, -1=abandoned
	FocusPhase  FocusPhase `gorm:"not null;default:0" json:"focus_phase"`                   // Runtime-owned Focus phase
	Checkpoint  string     `gorm:"type:text;not null;default:''" json:"checkpoint"`         // Compact runtime-owned recovery checkpoint
	CreatedAt   time.Time  `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for Work.
func (Work) TableName() string { return "works" }
