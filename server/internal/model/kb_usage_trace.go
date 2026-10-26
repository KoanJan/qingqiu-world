package model

import "time"

// KBUsageTrace records one knowledge-base retrieval performed by an agent
// tool. It is the stable KB-oriented trace used by later relation analysis,
// instead of parsing generic Focus interaction logs.
type KBUsageTrace struct {
	ID                    int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	WorkID                int64     `gorm:"not null;default:0;index" json:"work_id"`
	SessionID             int64     `gorm:"not null;default:0;index" json:"session_id"`
	Query                 string    `gorm:"type:text;not null;default:''" json:"query"`
	Reason                string    `gorm:"type:text;not null;default:''" json:"reason"`
	AuthorizedKBIDsJSON   string    `gorm:"type:text;not null;default:'[]'" json:"authorized_kb_ids_json"`
	RequestedKBID         int64     `gorm:"not null;default:0" json:"requested_kb_id"`
	DocumentFilter        string    `gorm:"type:text;not null;default:''" json:"document_filter"`
	EvidenceHandlesJSON   string    `gorm:"type:text;not null;default:'[]'" json:"evidence_handles_json"`
	ResultStatus          int       `gorm:"not null;default:0" json:"result_status"`
	ReturnedEvidenceCount int       `gorm:"not null;default:0" json:"returned_evidence_count"`
	ReturnedDocumentCount int       `gorm:"not null;default:0" json:"returned_document_count"`
	EvidenceBodyTruncated int       `gorm:"not null;default:0" json:"evidence_body_truncated"`
	QueryFingerprint      string    `gorm:"type:varchar(128);not null;default:''" json:"query_fingerprint"`
	ReasonFingerprint     string    `gorm:"type:varchar(128);not null;default:''" json:"reason_fingerprint"`
	CreatedAt             time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName returns the database table name for KBUsageTrace.
func (KBUsageTrace) TableName() string { return "kb_usage_traces" }
