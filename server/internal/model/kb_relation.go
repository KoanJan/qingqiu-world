package model

import "time"

// KBEntityState is the lifecycle state of a lightweight KB entity.
type KBEntityState int

const (
	// KBEntityStateActive means the entity can participate in relation lookup.
	KBEntityStateActive KBEntityState = 1
	// KBEntityStateStale means the entity may need label/scope revalidation.
	KBEntityStateStale KBEntityState = 2
	// KBEntityStateArchived means the entity is retained for audit only.
	KBEntityStateArchived KBEntityState = 3
)

// KBRelationState is the lifecycle state of an evidence-grounded relation.
type KBRelationState int

const (
	// KBRelationStateCandidate is extracted but not yet grounded.
	KBRelationStateCandidate KBRelationState = 1
	// KBRelationStateGrounded has valid supporting evidence but is not active.
	KBRelationStateGrounded KBRelationState = 2
	// KBRelationStateActive can participate in semantic expansion.
	KBRelationStateActive KBRelationState = 3
	// KBRelationStateStale points at changed or deleted evidence.
	KBRelationStateStale KBRelationState = 4
	// KBRelationStateRejected failed grounding or admission.
	KBRelationStateRejected KBRelationState = 5
	// KBRelationStateArchived is retained for audit and no longer maintained.
	KBRelationStateArchived KBRelationState = 6
)

// KBRelationJobType identifies one relation-maintenance task kind.
type KBRelationJobType int

const (
	// KBRelationJobTypeAnalyzeFocus extracts candidate relations from all KB usage in one workload.
	KBRelationJobTypeAnalyzeFocus KBRelationJobType = 1
	// KBRelationJobTypeRevalidateRelation rechecks stale relation evidence.
	KBRelationJobTypeRevalidateRelation KBRelationJobType = 2
)

// KBRelationJobState is the state of a background relation-maintenance job.
type KBRelationJobState int

const (
	// KBRelationJobStatePending is waiting to run.
	KBRelationJobStatePending KBRelationJobState = 1
	// KBRelationJobStateRunning is currently being processed.
	KBRelationJobStateRunning KBRelationJobState = 2
	// KBRelationJobStateCompleted finished successfully.
	KBRelationJobStateCompleted KBRelationJobState = 3
	// KBRelationJobStateFailed exhausted or recorded a recoverable failure.
	KBRelationJobStateFailed KBRelationJobState = 4
	// KBRelationJobStateSkipped was intentionally not processed.
	KBRelationJobStateSkipped KBRelationJobState = 5
)

// KBEntity is a lightweight, scope-bound entity identity used by relation
// traversal. It is not an ontology node and carries no type hierarchy.
type KBEntity struct {
	ID              int64         `gorm:"primaryKey;autoIncrement" json:"id"`
	ScopeHash       string        `gorm:"type:varchar(128);not null;uniqueIndex:idx_kb_entities_scope_label,priority:1" json:"scope_hash"`
	ScopeKBIDsJSON  string        `gorm:"type:text;not null;default:'[]'" json:"scope_kb_ids_json"`
	NormalizedLabel string        `gorm:"type:varchar(500);not null;uniqueIndex:idx_kb_entities_scope_label,priority:2" json:"normalized_label"`
	DisplayLabel    string        `gorm:"type:varchar(500);not null;default:''" json:"display_label"`
	State           KBEntityState `gorm:"not null;default:1;index" json:"state"`
	CreatedAt       time.Time     `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt       time.Time     `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for KBEntity.
func (KBEntity) TableName() string { return "kb_entities" }

// KBRelation is a derived semantic shortcut supported by KB evidence.
type KBRelation struct {
	ID              int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	ScopeHash       string `gorm:"type:varchar(128);not null;index" json:"scope_hash"`
	ScopeKBIDsJSON  string `gorm:"type:text;not null;default:'[]'" json:"scope_kb_ids_json"`
	SubjectEntityID int64  `gorm:"not null;index" json:"subject_entity_id"`
	Predicate       string `gorm:"type:varchar(255);not null;default:''" json:"predicate"`
	ObjectEntityID  int64  `gorm:"not null;index" json:"object_entity_id"`
	// ApplicabilityNote is a natural-language boundary for the proposition,
	// such as time range, version, project scope, or source assumption. It is
	// not a structured filter, confidence score, or proof.
	ApplicabilityNote string          `gorm:"type:text;not null;default:''" json:"applicability_note"`
	State             KBRelationState `gorm:"not null;default:1;index" json:"state"`
	IdempotencyKey    string          `gorm:"type:varchar(128);not null;uniqueIndex" json:"idempotency_key"`
	EvidenceHash      string          `gorm:"type:varchar(128);not null;default:''" json:"evidence_hash"`
	PolicyVersion     string          `gorm:"type:varchar(64);not null;default:''" json:"policy_version"`
	UseCount          int             `gorm:"not null;default:0" json:"use_count"`
	StaleReason       string          `gorm:"type:text;not null;default:''" json:"stale_reason"`
	LastUsedAtUnix    int64           `gorm:"not null;default:0" json:"last_used_at_unix"`
	CreatedAt         time.Time       `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt         time.Time       `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for KBRelation.
func (KBRelation) TableName() string { return "kb_relations" }

// KBRelationEvidence connects a derived relation to canonical active evidence.
type KBRelationEvidence struct {
	ID                  int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	RelationID          int64     `gorm:"not null;index;uniqueIndex:idx_kb_relation_evidence_unique,priority:1" json:"relation_id"`
	KnowledgeBaseID     int64     `gorm:"not null;index" json:"knowledge_base_id"`
	DocumentID          int64     `gorm:"not null;index" json:"document_id"`
	RevisionID          int64     `gorm:"not null;index" json:"revision_id"`
	ContentNodeID       int64     `gorm:"not null;index" json:"content_node_id"`
	ChunkID             int64     `gorm:"not null;default:0;index" json:"chunk_id"`
	LocatorJSON         string    `gorm:"type:text;not null;default:'{}'" json:"locator_json"`
	SupportQuote        string    `gorm:"type:text;not null;default:''" json:"support_quote"`
	ContentHash         string    `gorm:"type:varchar(128);not null;default:''" json:"content_hash"`
	EvidenceFingerprint string    `gorm:"type:varchar(128);not null;default:'';uniqueIndex:idx_kb_relation_evidence_unique,priority:2" json:"evidence_fingerprint"`
	CreatedAt           time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
}

// TableName returns the database table name for KBRelationEvidence.
func (KBRelationEvidence) TableName() string { return "kb_relation_evidence" }

// KBRelationJob records one pending or completed relation-maintenance task.
type KBRelationJob struct {
	ID             int64              `gorm:"primaryKey;autoIncrement" json:"id"`
	JobType        KBRelationJobType  `gorm:"not null;default:1;index" json:"job_type"`
	State          KBRelationJobState `gorm:"not null;default:1;index" json:"state"`
	SourceWorkID   int64              `gorm:"not null;default:0;index" json:"source_work_id"`
	RelationID     int64              `gorm:"not null;default:0;index" json:"relation_id"`
	IdempotencyKey string             `gorm:"type:varchar(128);not null;uniqueIndex" json:"idempotency_key"`
	PayloadJSON    string             `gorm:"type:text;not null;default:'{}'" json:"payload_json"`
	Attempts       int                `gorm:"not null;default:0" json:"attempts"`
	LastError      string             `gorm:"type:text;not null;default:''" json:"last_error"`
	CreatedAt      time.Time          `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt      time.Time          `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName returns the database table name for KBRelationJob.
func (KBRelationJob) TableName() string { return "kb_relation_jobs" }
