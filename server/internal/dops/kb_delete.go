package dops

import (
	"encoding/json"
	"fmt"
	"sort"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

// KBDeletionSummary reports the rows removed by the application-level KB
// cascade. The project intentionally avoids database foreign keys, so dops
// owns this atomic row deletion contract.
type KBDeletionSummary struct {
	Documents        int64
	Revisions        int64
	ContentNodes     int64
	ChunkNodes       int64
	Chunks           int64
	RelationJobs     int64
	Relations        int64
	RelationEvidence int64
	Entities         int64
	UsageTraces      int64
	AccessGrants     int64
}

// kbDeletionPlan captures all application-level dependencies that must be
// removed when a knowledge base is hard-deleted.
type kbDeletionPlan struct {
	DocumentIDs       []int64
	RevisionIDs       []int64
	ContentNodeIDs    []int64
	ChunkIDs          []int64
	RelationIDs       []int64
	RelationEntityIDs []int64
	UsageTraceIDs     []int64
	UsageWorkIDs      []int64
}

// DeleteKnowledgeBaseRows removes a KB and all DB rows owned by it in one
// transaction. Filesystem cleanup remains in the service layer because it is
// not part of the database atomicity boundary.
func DeleteKnowledgeBaseRows(kbID int64) (KBDeletionSummary, error) {
	var summary KBDeletionSummary
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		plan, err := buildKnowledgeBaseDeletionPlan(tx, kbID)
		if err != nil {
			return err
		}
		summary, err = deleteKnowledgeBaseRows(tx, kbID, plan)
		return err
	})
	if err != nil {
		return KBDeletionSummary{}, fmt.Errorf("delete KB database rows: %w", err)
	}
	return summary, nil
}

// buildKnowledgeBaseDeletionPlan resolves every row family that references the
// KB. Callers must execute the returned plan in the same transaction because
// document processing and relation maintenance may otherwise race the delete.
func buildKnowledgeBaseDeletionPlan(tx *gorm.DB, kbID int64) (kbDeletionPlan, error) {
	if kbID <= 0 {
		return kbDeletionPlan{}, fmt.Errorf("kb_id is required")
	}
	var kb model.KnowledgeBase
	if err := tx.Select("id").First(&kb, kbID).Error; err != nil {
		return kbDeletionPlan{}, fmt.Errorf("load knowledge base %d: %w", kbID, err)
	}

	var plan kbDeletionPlan
	if err := tx.Model(&model.Document{}).Where("knowledge_base_id = ?", kbID).Pluck("id", &plan.DocumentIDs).Error; err != nil {
		return plan, fmt.Errorf("load KB documents: %w", err)
	}
	if err := tx.Model(&model.DocumentChunk{}).Where("knowledge_base_id = ?", kbID).Pluck("id", &plan.ChunkIDs).Error; err != nil {
		return plan, fmt.Errorf("load KB chunks: %w", err)
	}
	if len(plan.DocumentIDs) > 0 {
		if err := tx.Model(&model.DocumentRevision{}).Where("document_id IN ?", plan.DocumentIDs).Pluck("id", &plan.RevisionIDs).Error; err != nil {
			return plan, fmt.Errorf("load KB document revisions: %w", err)
		}
		if err := tx.Model(&model.ContentNode{}).Where("document_id IN ?", plan.DocumentIDs).Pluck("id", &plan.ContentNodeIDs).Error; err != nil {
			return plan, fmt.Errorf("load KB content nodes: %w", err)
		}
	}
	if err := tx.Model(&model.KBRelationEvidence{}).Where("knowledge_base_id = ?", kbID).Distinct().Pluck("relation_id", &plan.RelationIDs).Error; err != nil {
		return plan, fmt.Errorf("load KB relation IDs: %w", err)
	}
	if len(plan.RelationIDs) > 0 {
		var subjectIDs []int64
		var objectIDs []int64
		if err := tx.Model(&model.KBRelation{}).Where("id IN ?", plan.RelationIDs).Pluck("subject_entity_id", &subjectIDs).Error; err != nil {
			return plan, fmt.Errorf("load KB relation subject entities: %w", err)
		}
		if err := tx.Model(&model.KBRelation{}).Where("id IN ?", plan.RelationIDs).Pluck("object_entity_id", &objectIDs).Error; err != nil {
			return plan, fmt.Errorf("load KB relation object entities: %w", err)
		}
		plan.RelationEntityIDs = uniqueInt64s(append(subjectIDs, objectIDs...))
	}

	traceIDs, workIDs, err := findKBUsageTraceDeletionTargets(tx, kbID)
	if err != nil {
		return plan, err
	}
	plan.UsageTraceIDs = traceIDs
	plan.UsageWorkIDs = workIDs

	return plan, nil
}

// deleteKnowledgeBaseRows executes the KB deletion plan inside the caller's
// transaction. Deletion order keeps derived rows ahead of their source rows and
// removes relation entities only when no remaining relation references them.
func deleteKnowledgeBaseRows(tx *gorm.DB, kbID int64, plan kbDeletionPlan) (KBDeletionSummary, error) {
	var summary KBDeletionSummary
	var err error

	if len(plan.UsageWorkIDs) > 0 {
		summary.RelationJobs, err = deleteRows(tx.Where("source_work_id IN ?", plan.UsageWorkIDs).Delete(&model.KBRelationJob{}), "delete KB workload relation jobs")
		if err != nil {
			return summary, err
		}
	}
	if len(plan.RelationIDs) > 0 {
		removed, err := deleteRows(tx.Where("relation_id IN ?", plan.RelationIDs).Delete(&model.KBRelationJob{}), "delete KB relation revalidation jobs")
		if err != nil {
			return summary, err
		}
		summary.RelationJobs += removed

		summary.RelationEvidence, err = deleteRows(tx.Where("relation_id IN ?", plan.RelationIDs).Delete(&model.KBRelationEvidence{}), "delete KB relation evidence")
		if err != nil {
			return summary, err
		}
		summary.Relations, err = deleteRows(tx.Where("id IN ?", plan.RelationIDs).Delete(&model.KBRelation{}), "delete KB relations")
		if err != nil {
			return summary, err
		}
	}
	if len(plan.RelationEntityIDs) > 0 {
		summary.Entities, err = deleteRows(tx.
			Where("id IN ?", plan.RelationEntityIDs).
			Where("id NOT IN (?)", tx.Model(&model.KBRelation{}).Select("subject_entity_id")).
			Where("id NOT IN (?)", tx.Model(&model.KBRelation{}).Select("object_entity_id")).
			Delete(&model.KBEntity{}), "delete orphan KB entities")
		if err != nil {
			return summary, err
		}
	}

	if len(plan.UsageTraceIDs) > 0 {
		summary.UsageTraces, err = deleteRows(tx.Where("id IN ?", plan.UsageTraceIDs).Delete(&model.KBUsageTrace{}), "delete KB usage traces")
		if err != nil {
			return summary, err
		}
	}
	if len(plan.ChunkIDs) > 0 {
		summary.ChunkNodes, err = deleteRows(tx.Where("chunk_id IN ?", plan.ChunkIDs).Delete(&model.DocumentChunkNode{}), "delete KB chunk-node mappings by chunk")
		if err != nil {
			return summary, err
		}
	}
	if len(plan.ContentNodeIDs) > 0 {
		removed, err := deleteRows(tx.Where("node_id IN ?", plan.ContentNodeIDs).Delete(&model.DocumentChunkNode{}), "delete KB chunk-node mappings by node")
		if err != nil {
			return summary, err
		}
		summary.ChunkNodes += removed

		summary.ContentNodes, err = deleteRows(tx.Where("id IN ?", plan.ContentNodeIDs).Delete(&model.ContentNode{}), "delete KB content nodes")
		if err != nil {
			return summary, err
		}
	}
	if len(plan.RevisionIDs) > 0 {
		summary.Revisions, err = deleteRows(tx.Where("id IN ?", plan.RevisionIDs).Delete(&model.DocumentRevision{}), "delete KB document revisions")
		if err != nil {
			return summary, err
		}
	}

	summary.Chunks, err = deleteRows(tx.Where("knowledge_base_id = ?", kbID).Delete(&model.DocumentChunk{}), "delete KB chunks")
	if err != nil {
		return summary, err
	}
	summary.Documents, err = deleteRows(tx.Where("knowledge_base_id = ?", kbID).Delete(&model.Document{}), "delete KB documents")
	if err != nil {
		return summary, err
	}
	summary.AccessGrants, err = deleteRows(tx.Where("kb_id = ?", kbID).Delete(&model.KBAccess{}), "delete KB access grants")
	if err != nil {
		return summary, err
	}
	if _, err := deleteRows(tx.Delete(&model.KnowledgeBase{}, kbID), "delete knowledge base"); err != nil {
		return summary, err
	}
	return summary, nil
}

// deleteRows normalizes GORM deletion errors with a business operation name.
func deleteRows(result *gorm.DB, operation string) (int64, error) {
	if result.Error != nil {
		return 0, fmt.Errorf("%s: %w", operation, result.Error)
	}
	return result.RowsAffected, nil
}

// findKBUsageTraceDeletionTargets returns usage traces that directly mention
// the deleted KB. JSON fields are parsed exactly after a coarse SQL prefilter.
func findKBUsageTraceDeletionTargets(tx *gorm.DB, kbID int64) ([]int64, []int64, error) {
	likeToken := fmt.Sprintf("%%%d%%", kbID)
	var traces []model.KBUsageTrace
	if err := tx.Select("id, work_id, requested_kb_id, authorized_kb_ids_json, evidence_handles_json").
		Where("requested_kb_id = ? OR authorized_kb_ids_json LIKE ? OR evidence_handles_json LIKE ?", kbID, likeToken, likeToken).
		Find(&traces).Error; err != nil {
		return nil, nil, fmt.Errorf("load KB usage traces: %w", err)
	}

	traceIDs := make([]int64, 0, len(traces))
	workIDSet := make(map[int64]struct{})
	for _, trace := range traces {
		if !usageTraceReferencesKB(trace, kbID) {
			continue
		}
		traceIDs = append(traceIDs, trace.ID)
		if trace.WorkID > 0 {
			workIDSet[trace.WorkID] = struct{}{}
		}
	}
	return uniqueInt64s(traceIDs), mapKeysInt64(workIDSet), nil
}

// usageTraceReferencesKB checks both explicit requested_kb_id and the compact
// JSON metadata stored by scan_kb. Malformed JSON is treated as related because
// it was already selected by the coarse KB-id prefilter and cannot be trusted.
func usageTraceReferencesKB(trace model.KBUsageTrace, kbID int64) bool {
	if trace.RequestedKBID == kbID {
		return true
	}
	var authorizedKBIDs []int64
	if err := json.Unmarshal([]byte(trace.AuthorizedKBIDsJSON), &authorizedKBIDs); err != nil {
		applogger.Warn("KB delete: malformed usage trace authorized KB IDs, deleting trace", "trace_id", trace.ID, "error", err)
		return true
	}
	if int64SliceContains(authorizedKBIDs, kbID) {
		return true
	}
	var handles []struct {
		KnowledgeBaseID int64 `json:"kb_id"`
	}
	if err := json.Unmarshal([]byte(trace.EvidenceHandlesJSON), &handles); err != nil {
		applogger.Warn("KB delete: malformed usage trace evidence handles, deleting trace", "trace_id", trace.ID, "error", err)
		return true
	}
	for _, handle := range handles {
		if handle.KnowledgeBaseID == kbID {
			return true
		}
	}
	return false
}

// uniqueInt64s returns the input IDs without duplicates while preserving the
// first-seen order for stable logs and deterministic tests.
func uniqueInt64s(values []int64) []int64 {
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
	return result
}

// mapKeysInt64 converts an ID set into a deterministic slice.
func mapKeysInt64(values map[int64]struct{}) []int64 {
	result := make([]int64, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i] < result[j]
	})
	return uniqueInt64s(result)
}

func int64SliceContains(values []int64, needle int64) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
