// Package model defines the database models for the application.
package model

import "time"

// Message represents a chat message in a session.
// PersonID identifies who sent the message (AI agent or human user).
// Assistant messages go through a streaming phase before being completed.
type Message struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID int64     `gorm:"not null;index;column:session_id" json:"session_id"`
	PersonID  int64     `gorm:"not null;index;column:person_id;default:0" json:"person_id"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for Message.
func (Message) TableName() string { return "messages" }
