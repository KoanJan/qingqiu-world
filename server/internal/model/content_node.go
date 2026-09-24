package model

import "time"

// ContentNode is a canonical structural node extracted from a document revision.
// ParentID uses zero for the root to avoid nullable database fields.
type ContentNode struct {
	ID           int64           `gorm:"primaryKey;autoIncrement" json:"id"`
	DocumentID   int64           `gorm:"not null;index" json:"document_id"`
	RevisionID   int64           `gorm:"not null;index:idx_content_nodes_revision_ordinal,priority:1" json:"revision_id"`
	ParentID     int64           `gorm:"not null;default:0;index:idx_content_nodes_parent_ordinal,priority:2" json:"parent_id"`
	Ordinal      int             `gorm:"not null;index:idx_content_nodes_revision_ordinal,priority:2;index:idx_content_nodes_parent_ordinal,priority:3" json:"ordinal"`
	NodeType     ContentNodeType `gorm:"not null;default:0" json:"node_type"`
	Text         string          `gorm:"type:text;not null;default:''" json:"text"`
	ContentHash  string          `gorm:"type:varchar(128);not null;default:''" json:"content_hash"`
	LocatorJSON  string          `gorm:"type:text;not null;default:'{}'" json:"locator_json"`
	MetadataJSON string          `gorm:"type:text;not null;default:'{}'" json:"metadata_json"`
	CreatedAt    time.Time       `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName returns the database table name for ContentNode.
func (ContentNode) TableName() string { return "content_nodes" }

// ContentNodeType identifies the deterministic structural role of a content
// node. It intentionally uses an integer-backed domain enum for persistence.
type ContentNodeType int

const (
	// ContentNodeTypeDocument is the single root node of a revision.
	ContentNodeTypeDocument ContentNodeType = iota
	// ContentNodeTypeParagraph stores a textual paragraph or legacy chunk.
	ContentNodeTypeParagraph
	// ContentNodeTypeHeading is a structural Markdown heading.
	ContentNodeTypeHeading
	// ContentNodeTypeList stores a contiguous Markdown list block.
	ContentNodeTypeList
	// ContentNodeTypeTable stores a contiguous Markdown table block.
	ContentNodeTypeTable
	// ContentNodeTypeCode stores a fenced or indented code block.
	ContentNodeTypeCode
	// ContentNodeTypeDocumentBody stores a small document's complete body.
	ContentNodeTypeDocumentBody
	// ContentNodeTypeAggregate stores contiguous short content as one leaf.
	ContentNodeTypeAggregate
	// ContentNodeTypePage is a source page container.
	ContentNodeTypePage
	// ContentNodeTypeListItem stores a deterministically parsed list item.
	ContentNodeTypeListItem
	// ContentNodeTypeTableRow stores a deterministically parsed table row.
	ContentNodeTypeTableRow
	// ContentNodeTypeCodeLine stores a deterministically parsed code line.
	ContentNodeTypeCodeLine
)
