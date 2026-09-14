package model

import "time"

// Interaction type constants.
const (
	InteractionTypeRequest  = 1 // Messages sent to the LLM
	InteractionTypeResponse = 2 // LLM output including thoughts, tool_calls, finish_reason
	InteractionTypeGuidance = 3 // External guidance directive (route/cancel) received during execution
)

// Interaction captures one step of the ReAct loop for agent-world interactions.
//
// Interactions are grouped by work_id, representing the Work that produced them.
// This directly models the relationship: a Work (focused-work execution) produces
// multiple iterations of interactions, independent of the message stream.
//
// Each iteration produces two records: a request (type=1) and a response (type=2).
type Interaction struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID int64     `gorm:"not null;index;column:session_id" json:"session_id"`
	WorkID    int64     `gorm:"not null;index;column:work_id" json:"work_id"` // References works.id
	Iteration int       `gorm:"not null" json:"iteration"`
	Type      int       `gorm:"not null" json:"type"`
	Data      string    `gorm:"type:text;not null" json:"data"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for Interaction.
func (Interaction) TableName() string { return "interactions" }
