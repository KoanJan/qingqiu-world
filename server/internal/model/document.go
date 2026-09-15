package model

import "time"

// Document represents an uploaded file in a knowledge base.
// Documents go through an async processing pipeline: pending → processing → ready/failed.
type Document struct {
	ID               int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	KnowledgeBaseID  int64     `gorm:"not null;index;column:knowledge_base_id" json:"knowledge_base_id"`
	Title            string    `gorm:"type:varchar(500);not null" json:"title"`
	SourceKind       int       `gorm:"not null;default:0" json:"source_kind"` // 0=local upload; the only implemented source kind.
	SourceURI        string    `gorm:"type:varchar(500);not null;default:''" json:"source_uri"`
	Source           string    `gorm:"type:varchar(500);not null;default:''" json:"source"`
	FilePath         string    `gorm:"type:varchar(500);not null;default:''" json:"file_path"`
	FileSize         int64     `gorm:"not null;default:0" json:"file_size"`
	FileType         string    `gorm:"type:varchar(20);not null;default:''" json:"file_type"`
	ChunkCount       int       `gorm:"not null;default:0" json:"chunk_count"`
	Status           int       `gorm:"not null;default:0" json:"status"` // 0=pending, 1=processing, 2=ready, 3=failed, 4=deleted
	ActiveRevisionID int64     `gorm:"not null;default:0;index" json:"active_revision_id"`
	ErrorMessage     string    `gorm:"type:text;not null;default:''" json:"error_message"`
	CreatedAt        time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt        time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

const (
	// DocumentSourceKindLocalUpload is the only source kind implemented in 0.1.13.
	DocumentSourceKindLocalUpload = 0
)

// TableName returns the database table name for Document.
func (Document) TableName() string { return "documents" }

// Document status constants
const (
	DocumentStatusPending    = 0 // Uploaded, waiting for processing
	DocumentStatusProcessing = 1 // Currently being chunked and embedded
	DocumentStatusReady      = 2 // Successfully processed and indexed
	DocumentStatusFailed     = 3 // Processing failed (see error_message)
	DocumentStatusDeleted    = 4 // Soft-deleted
)
