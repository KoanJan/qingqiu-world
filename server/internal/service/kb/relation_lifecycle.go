package kb

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

const (
	// DefaultRelationPolicyVersion identifies the first admission policy for
	// workload-derived KB relations.
	DefaultRelationPolicyVersion = "relation-policy-v1"
)

// RelationGroundingInput is the minimal evidence payload required before a
// relation candidate can become grounded.
type RelationGroundingInput struct {
	KnowledgeBaseID int64
	DocumentID      int64
	RevisionID      int64
	ContentNodeID   int64
	ChunkID         int64
	LocatorJSON     string
	SupportQuote    string
	ContentHash     string
}

// GroundedRelationInput is the admission-ready payload for a relation whose
// evidence handles must already come from canonical KB retrieval.
type GroundedRelationInput struct {
	ScopeKBIDs        []int64
	SubjectLabel      string
	Predicate         string
	ObjectLabel       string
	ApplicabilityNote string
	Evidence          []RelationGroundingInput
	PolicyVersion     string
}

// RelationScopeJSON returns a stable JSON array for a KB scope.
func RelationScopeJSON(kbIDs []int64) string {
	normalized := normalizedInt64Set(kbIDs)
	data, err := json.Marshal(normalized)
	if err != nil {
		applogger.Error("relation scope: failed to marshal KB scope", "kb_ids", kbIDs, "error", err)
		return "[]"
	}
	return string(data)
}

// RelationScopeHash returns a stable hash for an authorized KB scope.
func RelationScopeHash(kbIDs []int64) string {
	return hashRelationText("scope|" + RelationScopeJSON(kbIDs))
}

// NormalizeEntityLabel canonicalizes an entity label for lightweight
// same-scope deduplication. It does not infer ontology or type information.
func NormalizeEntityLabel(label string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(label)), " "))
}

// NormalizeRelationPredicate canonicalizes the open predicate vocabulary.
func NormalizeRelationPredicate(predicate string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(predicate)), "_"))
}

// RelationIdempotencyKey returns the deterministic key used to avoid duplicate
// relation rows for the same scoped subject-predicate-object admission policy.
func RelationIdempotencyKey(scopeHash string, subjectEntityID int64, predicate string, objectEntityID int64, policyVersion string) string {
	return hashRelationText(fmt.Sprintf("relation|%s|%d|%s|%d|%s",
		scopeHash,
		subjectEntityID,
		NormalizeRelationPredicate(predicate),
		objectEntityID,
		policyVersion,
	))
}

// RelationEvidenceFingerprint returns a stable fingerprint for one grounded
// evidence handle. It is independent of relation IDs so it can be computed
// before persistence.
func RelationEvidenceFingerprint(input RelationGroundingInput) string {
	return hashRelationText(fmt.Sprintf("evidence|%d|%d|%d|%d|%d|%s|%s",
		input.KnowledgeBaseID,
		input.DocumentID,
		input.RevisionID,
		input.ContentNodeID,
		input.ChunkID,
		input.ContentHash,
		input.SupportQuote,
	))
}

// ValidateRelationGrounding checks that candidate evidence still points to the
// active document revision and, when supplied, that the quote is present in the
// canonical node or retrieval unit. It returns the canonical node for callers
// that want to persist its locator/hash.
func ValidateRelationGrounding(input RelationGroundingInput) (*model.ContentNode, error) {
	if input.KnowledgeBaseID <= 0 || input.DocumentID <= 0 || input.RevisionID <= 0 || input.ContentNodeID <= 0 {
		return nil, fmt.Errorf("relation grounding requires kb/document/revision/content-node IDs")
	}

	var document model.Document
	if err := database.DB.Select("id, knowledge_base_id, status, active_revision_id").
		Where("id = ?", input.DocumentID).First(&document).Error; err != nil {
		return nil, fmt.Errorf("load grounding document: %w", err)
	}
	if document.KnowledgeBaseID != input.KnowledgeBaseID {
		return nil, fmt.Errorf("grounding document belongs to KB %d, not KB %d", document.KnowledgeBaseID, input.KnowledgeBaseID)
	}
	if document.Status != model.DocumentStatusReady || document.ActiveRevisionID != input.RevisionID {
		return nil, fmt.Errorf("grounding revision is not active: document status=%d active_revision_id=%d candidate_revision_id=%d", document.Status, document.ActiveRevisionID, input.RevisionID)
	}

	var node model.ContentNode
	if err := database.DB.Where("id = ? AND document_id = ? AND revision_id = ?", input.ContentNodeID, input.DocumentID, input.RevisionID).
		First(&node).Error; err != nil {
		return nil, fmt.Errorf("load grounding content node: %w", err)
	}
	if input.ContentHash != "" && node.ContentHash != input.ContentHash {
		return nil, fmt.Errorf("grounding content hash mismatch")
	}

	quoteCarrier := node.Text
	if input.ChunkID > 0 {
		var chunk model.DocumentChunk
		if err := database.DB.Where("id = ? AND knowledge_base_id = ? AND document_id = ? AND revision_id = ? AND deleted = 0",
			input.ChunkID, input.KnowledgeBaseID, input.DocumentID, input.RevisionID).First(&chunk).Error; err != nil {
			return nil, fmt.Errorf("load grounding retrieval unit: %w", err)
		}
		var mappingCount int64
		if err := database.DB.Model(&model.DocumentChunkNode{}).
			Where("chunk_id = ? AND node_id = ?", input.ChunkID, input.ContentNodeID).
			Count(&mappingCount).Error; err != nil {
			return nil, fmt.Errorf("verify chunk-node grounding: %w", err)
		}
		if mappingCount == 0 {
			return nil, fmt.Errorf("grounding chunk is not mapped to content node")
		}
		quoteCarrier = chunk.DisplayText
		if quoteCarrier == "" {
			quoteCarrier = chunk.Content
		}
	}

	if quote := strings.TrimSpace(input.SupportQuote); quote != "" && !strings.Contains(quoteCarrier, quote) {
		return nil, fmt.Errorf("grounding quote is not present in canonical evidence")
	}
	return &node, nil
}

// UpsertGroundedRelation validates evidence and persists a grounded relation
// idempotently. It does not make the relation active; activation belongs to the
// later admission/maintenance phase.
func UpsertGroundedRelation(input GroundedRelationInput) (*model.KBRelation, error) {
	subjectLabel := NormalizeEntityLabel(input.SubjectLabel)
	predicate := NormalizeRelationPredicate(input.Predicate)
	objectLabel := NormalizeEntityLabel(input.ObjectLabel)
	applicabilityNote := sanitizeApplicabilityNote(input.ApplicabilityNote)
	if subjectLabel == "" || predicate == "" || objectLabel == "" {
		return nil, fmt.Errorf("relation subject, predicate and object are required")
	}
	if len(input.Evidence) == 0 {
		return nil, fmt.Errorf("grounded relation requires at least one evidence handle")
	}
	policyVersion := input.PolicyVersion
	if policyVersion == "" {
		policyVersion = DefaultRelationPolicyVersion
	}
	scopeKBIDs := input.ScopeKBIDs
	if len(scopeKBIDs) == 0 {
		scopeKBIDs = make([]int64, 0, len(input.Evidence))
		for _, evidence := range input.Evidence {
			scopeKBIDs = append(scopeKBIDs, evidence.KnowledgeBaseID)
		}
	}
	scopeJSON := RelationScopeJSON(scopeKBIDs)
	scopeHash := RelationScopeHash(scopeKBIDs)

	validated := make([]RelationGroundingInput, 0, len(input.Evidence))
	fingerprints := make([]string, 0, len(input.Evidence))
	evidenceFingerprints := make([]string, 0, len(input.Evidence))
	for _, evidence := range input.Evidence {
		node, err := ValidateRelationGrounding(evidence)
		if err != nil {
			applogger.Warn("relation admission rejected ungrounded evidence", "document_id", evidence.DocumentID, "revision_id", evidence.RevisionID, "content_node_id", evidence.ContentNodeID, "error", err)
			return nil, err
		}
		copyEvidence := evidence
		if copyEvidence.LocatorJSON == "" {
			copyEvidence.LocatorJSON = node.LocatorJSON
		}
		if copyEvidence.ContentHash == "" {
			copyEvidence.ContentHash = node.ContentHash
		}
		validated = append(validated, copyEvidence)
		fingerprint := RelationEvidenceFingerprint(copyEvidence)
		fingerprints = append(fingerprints, fingerprint)
		evidenceFingerprints = append(evidenceFingerprints, fingerprint)
	}
	sort.Strings(fingerprints)
	evidenceHash := hashRelationText("relation-evidence|" + strings.Join(fingerprints, "|"))

	var relation model.KBRelation
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		subject, err := ensureKBEntity(tx, scopeHash, scopeJSON, subjectLabel, strings.TrimSpace(input.SubjectLabel))
		if err != nil {
			return err
		}
		object, err := ensureKBEntity(tx, scopeHash, scopeJSON, objectLabel, strings.TrimSpace(input.ObjectLabel))
		if err != nil {
			return err
		}
		key := RelationIdempotencyKey(scopeHash, subject.ID, predicate, object.ID, policyVersion)
		create := model.KBRelation{
			ScopeHash:         scopeHash,
			ScopeKBIDsJSON:    scopeJSON,
			SubjectEntityID:   subject.ID,
			Predicate:         predicate,
			ObjectEntityID:    object.ID,
			ApplicabilityNote: applicabilityNote,
			State:             model.KBRelationStateGrounded,
			IdempotencyKey:    key,
			EvidenceHash:      evidenceHash,
			PolicyVersion:     policyVersion,
		}
		if err := tx.Where("idempotency_key = ?", key).Attrs(create).FirstOrCreate(&relation).Error; err != nil {
			return fmt.Errorf("upsert relation: %w", err)
		}
		relationUpdates := map[string]interface{}{}
		if relation.EvidenceHash != evidenceHash {
			relationUpdates["evidence_hash"] = evidenceHash
		}
		mergedApplicabilityNote := mergeApplicabilityNotes(relation.ApplicabilityNote, applicabilityNote)
		if mergedApplicabilityNote != relation.ApplicabilityNote {
			relationUpdates["applicability_note"] = mergedApplicabilityNote
		}
		if relation.State == model.KBRelationStateStale || relation.State == model.KBRelationStateRejected {
			relationUpdates["state"] = model.KBRelationStateGrounded
			relationUpdates["stale_reason"] = ""
		}
		if len(relationUpdates) > 0 {
			if err := tx.Model(&model.KBRelation{}).Where("id = ?", relation.ID).Updates(relationUpdates).Error; err != nil {
				return fmt.Errorf("update grounded relation row: %w", err)
			}
			if err := tx.First(&relation, relation.ID).Error; err != nil {
				return fmt.Errorf("reload grounded relation row: %w", err)
			}
		}
		for index, evidence := range validated {
			row := model.KBRelationEvidence{
				RelationID:          relation.ID,
				KnowledgeBaseID:     evidence.KnowledgeBaseID,
				DocumentID:          evidence.DocumentID,
				RevisionID:          evidence.RevisionID,
				ContentNodeID:       evidence.ContentNodeID,
				ChunkID:             evidence.ChunkID,
				LocatorJSON:         nonEmptyString(evidence.LocatorJSON, "{}"),
				SupportQuote:        evidence.SupportQuote,
				ContentHash:         evidence.ContentHash,
				EvidenceFingerprint: evidenceFingerprints[index],
			}
			if err := tx.Where("relation_id = ? AND evidence_fingerprint = ?", relation.ID, row.EvidenceFingerprint).
				Attrs(row).FirstOrCreate(&row).Error; err != nil {
				return fmt.Errorf("upsert relation evidence: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &relation, nil
}

// ensureKBEntity creates or reuses one active scope-bound entity identity.
func ensureKBEntity(tx *gorm.DB, scopeHash, scopeJSON, normalizedLabel, displayLabel string) (*model.KBEntity, error) {
	if displayLabel == "" {
		displayLabel = normalizedLabel
	}
	entity := model.KBEntity{
		ScopeHash:       scopeHash,
		ScopeKBIDsJSON:  scopeJSON,
		NormalizedLabel: normalizedLabel,
		DisplayLabel:    displayLabel,
		State:           model.KBEntityStateActive,
	}
	if err := tx.Where("scope_hash = ? AND normalized_label = ?", scopeHash, normalizedLabel).
		Attrs(entity).FirstOrCreate(&entity).Error; err != nil {
		return nil, fmt.Errorf("ensure KB entity %q: %w", normalizedLabel, err)
	}
	return &entity, nil
}

// nonEmptyString supplies a non-null persistence fallback for optional text.
func nonEmptyString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// sanitizeApplicabilityNote preserves the free-form boundary text while keeping
// surrounding whitespace out of durable relation metadata. The note is
// advisory provenance, not a computable condition and not evidence.
func sanitizeApplicabilityNote(value string) string {
	return strings.TrimSpace(value)
}

// mergeApplicabilityNotes preserves previously admitted boundaries when the
// same scoped subject-predicate-object relation is grounded again. This stays
// intentionally natural-language: highly variable applicability constraints are
// not forced into a brittle schema.
func mergeApplicabilityNotes(existing, incoming string) string {
	existing = sanitizeApplicabilityNote(existing)
	incoming = sanitizeApplicabilityNote(incoming)
	if existing == "" {
		return incoming
	}
	if incoming == "" || existing == incoming || strings.Contains(existing, incoming) {
		return existing
	}
	if strings.Contains(incoming, existing) {
		return incoming
	}
	return existing + "\nAdditional applicability: " + incoming
}

// MarkRelationsStaleForDocument marks every relation supported by a document
// as stale. It is used before hard deletion and other evidence-invalidating
// operations.
func MarkRelationsStaleForDocument(documentID int64, reason string) error {
	if documentID <= 0 {
		return fmt.Errorf("document_id is required")
	}
	subQuery := database.DB.Model(&model.KBRelationEvidence{}).
		Select("relation_id").
		Where("document_id = ?", documentID)
	return markRelationsStaleBySubquery(subQuery, reason, "document_id", documentID)
}

// MarkRelationsStaleForSupersededDocumentRevisions marks relations supported
// by older revisions stale after a new active revision becomes visible.
func MarkRelationsStaleForSupersededDocumentRevisions(documentID, activeRevisionID int64, reason string) error {
	if documentID <= 0 || activeRevisionID <= 0 {
		return fmt.Errorf("document_id and active_revision_id are required")
	}
	subQuery := database.DB.Model(&model.KBRelationEvidence{}).
		Select("relation_id").
		Where("document_id = ? AND revision_id <> ?", documentID, activeRevisionID)
	return markRelationsStaleBySubquery(subQuery, reason, "document_id", documentID)
}

// RevalidateStaleRelation rechecks every evidence row supporting one stale
// relation. Valid evidence returns the relation to grounded; invalid evidence
// archives it so it cannot participate in future retrieval.
func RevalidateStaleRelation(relationID int64) error {
	if relationID <= 0 {
		return fmt.Errorf("relation_id is required")
	}
	var relation model.KBRelation
	if err := database.DB.First(&relation, relationID).Error; err != nil {
		return fmt.Errorf("load relation: %w", err)
	}
	if relation.State != model.KBRelationStateStale {
		return nil
	}
	var evidenceRows []model.KBRelationEvidence
	if err := database.DB.Where("relation_id = ?", relationID).Find(&evidenceRows).Error; err != nil {
		return fmt.Errorf("load relation evidence: %w", err)
	}
	if len(evidenceRows) == 0 {
		return archiveRelation(relationID, "revalidation found no evidence")
	}
	for _, row := range evidenceRows {
		_, err := ValidateRelationGrounding(RelationGroundingInput{
			KnowledgeBaseID: row.KnowledgeBaseID,
			DocumentID:      row.DocumentID,
			RevisionID:      row.RevisionID,
			ContentNodeID:   row.ContentNodeID,
			ChunkID:         row.ChunkID,
			LocatorJSON:     row.LocatorJSON,
			SupportQuote:    row.SupportQuote,
			ContentHash:     row.ContentHash,
		})
		if err != nil {
			return archiveRelation(relationID, fmt.Sprintf("revalidation failed: %v", err))
		}
	}
	if err := database.DB.Model(&model.KBRelation{}).Where("id = ? AND state = ?", relationID, model.KBRelationStateStale).
		Updates(map[string]interface{}{
			"state":        model.KBRelationStateGrounded,
			"stale_reason": "",
		}).Error; err != nil {
		return fmt.Errorf("restore grounded relation: %w", err)
	}
	applogger.Info("relation lifecycle: revalidated stale relation", "relation_id", relationID)
	return nil
}

// archiveRelation moves a relation out of all retrieval paths while retaining
// its audit row and reason.
func archiveRelation(relationID int64, reason string) error {
	if err := database.DB.Model(&model.KBRelation{}).Where("id = ?", relationID).
		Updates(map[string]interface{}{
			"state":        model.KBRelationStateArchived,
			"stale_reason": reason,
		}).Error; err != nil {
		return fmt.Errorf("archive stale relation: %w", err)
	}
	applogger.Info("relation lifecycle: archived relation", "relation_id", relationID, "reason", reason)
	return nil
}

// markRelationsStaleBySubquery applies stale state to all relation rows
// supported by the supplied evidence subquery and queues bounded revalidation.
func markRelationsStaleBySubquery(subQuery *gorm.DB, reason, logKey string, logValue int64) error {
	staleableStates := []model.KBRelationState{
		model.KBRelationStateCandidate,
		model.KBRelationStateGrounded,
		model.KBRelationStateActive,
	}
	var relationIDs []int64
	if err := database.DB.Model(&model.KBRelation{}).
		Where("id IN (?) AND state IN ?", subQuery, staleableStates).
		Pluck("id", &relationIDs).Error; err != nil {
		applogger.Error("relation lifecycle: failed to load stale relation IDs", logKey, logValue, "reason", reason, "error", err)
		return err
	}
	if len(relationIDs) == 0 {
		return nil
	}
	updates := map[string]interface{}{
		"state":        model.KBRelationStateStale,
		"stale_reason": reason,
	}
	result := database.DB.Model(&model.KBRelation{}).
		Where("id IN ?", relationIDs).
		Updates(updates)
	if result.Error != nil {
		applogger.Error("relation lifecycle: failed to mark relations stale", logKey, logValue, "reason", reason, "error", result.Error)
		return result.Error
	}
	if result.RowsAffected > 0 {
		applogger.Info("relation lifecycle: marked relations stale", logKey, logValue, "reason", reason, "count", result.RowsAffected)
	}
	for _, relationID := range relationIDs {
		if err := EnqueueRelationRevalidationJob(relationID); err != nil {
			applogger.Error("relation lifecycle: failed to enqueue revalidation", "relation_id", relationID, "error", err)
		}
	}
	return nil
}

// normalizedInt64Set removes non-positive IDs, de-duplicates and sorts them
// for stable scope hashes and deterministic query arguments.
func normalizedInt64Set(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// hashRelationText returns the stable prefixed SHA-256 representation used by
// relation idempotency, scope and evidence fingerprints.
func hashRelationText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:v1:%x", sum[:])
}
