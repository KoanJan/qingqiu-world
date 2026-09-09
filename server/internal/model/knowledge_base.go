package model

import "time"

// KnowledgeBaseIndexType constants.
type KnowledgeBaseIndexType int

const (
	// KnowledgeBaseIndexTypeFlat uses brute-force cosine similarity search.
	KnowledgeBaseIndexTypeFlat KnowledgeBaseIndexType = iota
	// KnowledgeBaseIndexTypeSwitching indicates the index is transitioning from flat to HNSW.
	KnowledgeBaseIndexTypeSwitching
	// KnowledgeBaseIndexTypeHNSW uses approximate nearest neighbor search.
	KnowledgeBaseIndexTypeHNSW
)

// KnowledgeBase represents a knowledge base that stores and indexes documents.
// Each knowledge base has its own vector storage (SQLite) and HNSW index file.
type KnowledgeBase struct {
	ID            int64                  `gorm:"primaryKey;autoIncrement" json:"id"`
	Name          string                 `gorm:"type:varchar(255);not null" json:"name"`
	Description   string                 `gorm:"type:text;not null;default:''" json:"description"`
	IndexType     KnowledgeBaseIndexType `gorm:"not null;default:0" json:"index_type"` // 0=flat, 1=switching, 2=hnsw
	IndexFilePath string                 `gorm:"type:varchar(500);not null;default:''" json:"index_file_path"`
	// KeywordRatio is the weight (alpha) of the BM25 keyword score in hybrid
	// retrieval: score = (1-ratio)*vector + ratio*keyword. 0 = vector only,
	// 1 = keyword only.
	KeywordRatio  float64 `gorm:"not null;default:0.3" json:"keyword_ratio"`
	DocumentCount int     `gorm:"not null;default:0" json:"document_count"`
	VectorCount   int     `gorm:"not null;default:0" json:"vector_count"`
	DeletedCount  int     `gorm:"not null;default:0" json:"deleted_count"`
	CreatedAt     time.Time              `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time              `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for KnowledgeBase.
func (KnowledgeBase) TableName() string { return "knowledge_bases" }
