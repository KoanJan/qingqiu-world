package model

import "time"

// Work status constants.
const (
	WorkStatusRunning   = 0  // Work is currently executing
	WorkStatusCompleted = 1  // Work finished successfully
	WorkStatusFailed    = 2  // Work finished but the task reported failure
	WorkStatusAbandoned = -1 // Work was abandoned (e.g., user correction, cancellation)
)

// Work represents a unit of work for an agent within a session.
//
// A Work is created when an agent decides to act on an event, and it may
// absorb subsequent events (e.g., user corrections) during its execution.
// Since 0.1.4, only TaskWork (ReAct loop) remains; ChatWork is retired.
//
// Three-layer model: Agent (long-lived entity) → Work (coherent goal) → Iteration (atomic ReAct step)
type Work struct {
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID    int64     `gorm:"not null;index:idx_person_status" json:"person_id"`
	SessionID   int64     `gorm:"not null" json:"session_id"`
	Description string    `gorm:"type:text;not null" json:"description"`                   // Natural language description for semantic routing and recovery
	Status      int       `gorm:"not null;default:0;index:idx_agent_status" json:"status"` // 0=running, 1=completed, -1=abandoned
	CreatedAt   time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt   time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for Work.
func (Work) TableName() string { return "works" }
