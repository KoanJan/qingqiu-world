package model

import "time"

// PSDigest stores an agent's private-space session digest — a short
// natural-language summary of one private-space loop run, focusing on the
// decisions and ideas explored rather than private file contents.
//
// Digest is the payload behind a memory event (events.event_type =
// EventTypePSDigest); each loop termination produces exactly one record,
// giving the agent's global cognition a recallable episodic trace of its
// default-mode activity.
type PSDigest struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	PersonID  int64     `gorm:"not null;index;column:person_id" json:"person_id"`
	Digest    string    `gorm:"type:text;not null" json:"digest"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName returns the database table name for PSDigest.
func (PSDigest) TableName() string { return "ps_digests" }
