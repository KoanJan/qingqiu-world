package model

import "time"

// DocumentRevision stores one immutable canonical rendition of a Document.
// ActiveRevisionID on Document controls which revision is visible to retrieval.
type DocumentRevision struct {
	ID                   int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	DocumentID           int64     `gorm:"not null;index" json:"document_id"`
	ContentHash          string    `gorm:"type:varchar(128);not null" json:"content_hash"`
	ParserID             string    `gorm:"type:varchar(64);not null;default:''" json:"parser_id"`
	ParserVersion        string    `gorm:"type:varchar(64);not null;default:''" json:"parser_version"`
	NormalizerVersion    string    `gorm:"type:varchar(64);not null;default:''" json:"normalizer_version"`
	CanonicalFingerprint string    `gorm:"type:varchar(128);not null;default:'';uniqueIndex:idx_document_revision_fingerprint" json:"canonical_fingerprint"`
	BlobPath             string    `gorm:"type:varchar(500);not null;default:''" json:"blob_path"`
	Status               int       `gorm:"not null;default:0;index" json:"status"`
	ErrorMessage         string    `gorm:"type:text;not null;default:''" json:"error_message"`
	ProvenanceStatus     int       `gorm:"not null;default:0;index" json:"provenance_status"`
	ProvenanceError      string    `gorm:"type:text;not null;default:''" json:"provenance_error"`
	CreatedAt            time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt            time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

const (
	// DocumentRevisionProvenanceUnknown identifies a legacy revision that has
	// not yet been verified against its source rendition.
	DocumentRevisionProvenanceUnknown = 0
	// DocumentRevisionProvenanceVerified means every retrieval unit has a
	// complete, source-aligned locator.
	DocumentRevisionProvenanceVerified = 1
	// DocumentRevisionProvenanceRepairRequired records an analysis failure. It
	// is intentionally not retried at startup; a new upload/revision is needed.
	DocumentRevisionProvenanceRepairRequired = 2
)

// TableName returns the database table name for DocumentRevision.
func (DocumentRevision) TableName() string { return "document_revisions" }

const (
	// DocumentRevisionStatusPending is waiting to be processed.
	DocumentRevisionStatusPending = 0
	// DocumentRevisionStatusProcessing is being parsed and represented.
	DocumentRevisionStatusProcessing = 1
	// DocumentRevisionStatusReady has persisted canonical and retrieval data.
	DocumentRevisionStatusReady = 2
	// DocumentRevisionStatusFailed records a recoverable processing failure.
	DocumentRevisionStatusFailed = 3
)
