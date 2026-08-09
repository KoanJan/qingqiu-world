package model

import "time"

// Work status constants.
const (
	WorkStatusRunning   = 0  // Work is currently executing
	WorkStatusCompleted = 1  // Work finished successfully
	WorkStatusAbandoned = -1 // Work was abandoned (e.g., user correction, cancellation)
)

// WorkType represents the type of work execution strategy.
type WorkType int

const (
	// WorkTypeChat is retired as of 0.1.4 — Chat actions no longer create Work records.
	// Kept for backward compatibility with existing database rows.
	WorkTypeChat WorkType = 1 // Retired since 0.1.4
	// WorkTypeTask represents a ReAct loop (multi-iteration with tool calls) work type.
	WorkTypeTask WorkType = 2 // ReAct loop (multi-iteration with tool calls)
)

// Work represents a unit of work for an agent within a session.
//
// A Work is created when an agent decides to act on an event, and it may
// absorb subsequent events (e.g., user corrections) during its execution.
// Work unifies the Chat path (single LLM call) and Task path (ReAct loop).
//
// Three-layer model: Agent (long-lived entity) → Work (coherent goal) → Iteration (atomic ReAct step)
type Work struct {
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID    int64     `gorm:"not null;index:idx_person_status" json:"person_id"`
	SessionID   int64     `gorm:"not null" json:"session_id"`
	Type        WorkType  `gorm:"not null" json:"type"`                                    // 1=chat, 2=task
	Description string    `gorm:"type:text;not null" json:"description"`                   // Natural language description for semantic routing and recovery
	Status      int       `gorm:"not null;default:0;index:idx_agent_status" json:"status"` // 0=running, 1=completed, -1=abandoned
	CreatedAt   time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt   time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for Work.
func (Work) TableName() string { return "works" }
