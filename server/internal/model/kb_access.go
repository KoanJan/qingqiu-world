package model

import "time"

// KBAccess represents an authorization record granting an AI person (agent)
// access to a knowledge base.
//
// Since 0.1.9 knowledge bases are global resources (a "library") decoupled
// from agents: this table only controls whether an agent MAY consult a KB,
// while the actual decision to search is made by the agent at comprehension
// time. One row per (person, KB) pair, enforced by a composite unique index.
type KBAccess struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID  int64     `gorm:"not null;uniqueIndex:idx_kb_access_person_kb;column:person_id" json:"person_id"`
	KBID      int64     `gorm:"not null;uniqueIndex:idx_kb_access_person_kb;index;column:kb_id" json:"kb_id"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for KBAccess.
func (KBAccess) TableName() string { return "kb_access" }
