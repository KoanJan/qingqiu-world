package model

import "time"

// SessionStatus records whether a conversation still accepts new messages.
type SessionStatus int

const (
	SessionStatusActive SessionStatus = iota + 1
	SessionStatusDeleted
)

// Session represents a conversation session. Participant relationships
// are tracked via the participant_sessions join table.
type Session struct {
	ID        int64         `gorm:"primaryKey;autoIncrement" json:"id"`
	Title     string        `gorm:"type:varchar(255);not null;default:''" json:"title"`
	Status    SessionStatus `gorm:"not null;default:1" json:"status"`
	CreatedAt time.Time     `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time     `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for Session.
func (Session) TableName() string { return "sessions" }
