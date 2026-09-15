package model

import "time"

// DocumentChunk represents a text segment split from a document.
// Each chunk is associated with a vector embedding stored in the per-KB SQLite file.
type DocumentChunk struct {
	ID               int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	KnowledgeBaseID  int64     `gorm:"not null;index;column:knowledge_base_id" json:"knowledge_base_id"`
	DocumentID       int64     `gorm:"not null;index;column:document_id" json:"document_id"`
	RevisionID       int64     `gorm:"not null;default:0;index" json:"revision_id"`
	VectorID         int64     `gorm:"not null;default:0" json:"vector_id"` // vector table id, 0=not embedded; status marker only, not used in retrieval
	ChunkIndex       int       `gorm:"not null" json:"chunk_index"`
	Content          string    `gorm:"type:text;not null" json:"content"`
	SearchText       string    `gorm:"type:text;not null;default:''" json:"search_text"`
	DisplayText      string    `gorm:"type:text;not null;default:''" json:"display_text"`
	TokenCount       int       `gorm:"not null;default:0" json:"token_count"`
	InputFingerprint string    `gorm:"type:varchar(128);not null;default:''" json:"input_fingerprint"`
	StartOffset      int       `gorm:"not null;default:0" json:"start_offset"`
	EndOffset        int       `gorm:"not null;default:0" json:"end_offset"`
	Deleted          int       `gorm:"not null;default:0" json:"deleted"` // logical bool: 0=active, 1=deleted
	CreatedAt        time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName returns the database table name for DocumentChunk.
func (DocumentChunk) TableName() string { return "document_chunks" }
