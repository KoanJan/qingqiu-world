package model

import "time"

// Person type constants.
const (
	PersonTypeAI    = 1 // AI agent
	PersonTypeHuman = 2 // Human user
)

// Person represents an identity in the system — either an AI agent or a human user.
// Name is the immutable identity anchor used in all narratives and prompts.
// Agents and users share the same identity namespace; name uniqueness is guaranteed
// across both types.
type Person struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name      string    `gorm:"type:varchar(255);not null;uniqueIndex" json:"name"` // Immutable identity key
	Bio       string    `gorm:"type:text;not null;default:''" json:"bio"`
	Avatar    string    `gorm:"type:varchar(500);not null;default:''" json:"avatar"` // Relative path under QingqiuWorldData/avatars/
	Type      int       `gorm:"not null" json:"type"`                                // 1=AI, 2=Human
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for Person.
func (Person) TableName() string { return "persons" }
