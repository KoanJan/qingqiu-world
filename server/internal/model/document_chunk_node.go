package model

// DocumentChunkNode maps a retrieval unit to its canonical content nodes.
// It deliberately has no foreign keys; service code owns referential cleanup.
type DocumentChunkNode struct {
	ChunkID int64 `gorm:"primaryKey;not null" json:"chunk_id"`
	NodeID  int64 `gorm:"primaryKey;not null;index" json:"node_id"`
	Ordinal int   `gorm:"not null;default:0" json:"ordinal"`
}

// TableName returns the database table name for DocumentChunkNode.
func (DocumentChunkNode) TableName() string { return "document_chunk_nodes" }
