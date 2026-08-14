package model

import "time"

// Jinshu (锦书) records a file delivery from one person to another, independent
// of any session. A single send copies the source files twice: one copy into
// the sender's sent/{id}/ directory and one into the recipient's received/{id}/
// directory, both under the jinshu root keyed by person id. The file location is
// derived from the jinshu root, the owning person id, and this record's
// auto-increment id, so no paths need to be stored here.
type Jinshu struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	FromPersonID int64     `gorm:"not null;index:idx_jinshu_from;column:from_person_id" json:"from_person_id"`
	ToPersonID   int64     `gorm:"not null;index:idx_jinshu_to;column:to_person_id" json:"to_person_id"`
	Topic        string    `gorm:"type:varchar(255);not null;default:'';column:topic" json:"topic"`
	Description  string    `gorm:"type:text;not null;default:'';column:description" json:"description"`
	IsRead       bool      `gorm:"not null;default:false;column:is_read" json:"is_read"` // Receiver-only read flag (only visible to the recipient)
	CreatedAt    time.Time `gorm:"not null;autoCreateTime;column:created_at" json:"created_at"`
}

// TableName returns the database table name for Jinshu.
func (Jinshu) TableName() string { return "jinshus" }
