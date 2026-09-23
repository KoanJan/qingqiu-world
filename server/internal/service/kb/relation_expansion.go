package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

const (
	// DefaultRelationExpansionDepth keeps scan_kb relation traversal shallow.
	// Focus can call scan_kb again when it needs another investigative step.
	DefaultRelationExpansionDepth = 1
	// DefaultRelationExpansionFanout caps relation rows considered from one
	// frontier so a dense graph cannot swamp vector-retrieved anchors.
	DefaultRelationExpansionFanout = 12
	// DefaultRelationExpansionEvidenceBudget caps evidence added by semantic
	// expansion. It does not cap the original retrieval anchors or metadata.
	DefaultRelationExpansionEvidenceBudget = 30
)

// RelationExpansionOptions bounds deterministic relation traversal.
type RelationExpansionOptions struct {
	Depth          int
	Fanout         int
	EvidenceBudget int
}

// relationExpansionStats aggregates the deterministic selection-policy and
// output-contract counters emitted at Info level, so the necessary metric
// points stay observable in production even when debug logging is disabled.
// Unauthorized/stale leakage counts are tripwires: they are expected to stay
// zero, and any non-zero value indicates a policy regression that was blocked
// by the final output audit (which also drops or scrubs the relation paths
// referencing the blocked evidence).
type relationExpansionStats struct {
	RelationsConsidered            int
	RelationsSkippedUnauthorized   int
	RelationsSkippedInactiveEntity int
	RelationsSkippedNoEvidence     int
	EvidenceSkippedStaleOrInvalid  int
	UnauthorizedLeakageBlocked     int
	StaleLeakageBlocked            int
	RelationPathsDroppedByAudit    int
	EvidenceDroppedByPathAudit     int
}

// RelationPath is retrieval provenance for relation-expanded evidence. It is
// not a conclusion and must not be treated as evidence by itself.
type RelationPath struct {
	RelationID          int64   `json:"relation_id"`
	Depth               int     `json:"depth"`
	SubjectEntityID     int64   `json:"subject_entity_id"`
	SubjectLabel        string  `json:"subject_label"`
	Predicate           string  `json:"predicate"`
	ObjectEntityID      int64   `json:"object_entity_id"`
	ObjectLabel         string  `json:"object_label"`
	ApplicabilityNote   string  `json:"applicability_note"`
	SourceChunkIDs      []int64 `json:"source_chunk_ids"`
	EvidenceChunkIDs    []int64 `json:"evidence_chunk_ids"`
	RelationEvidenceIDs []int64 `json:"relation_evidence_ids"`
}

// expandRelationsFromEvidence traverses only active/grounded relations whose
// scope is a subset of the caller-authorized KBs. Relations are hints: every
// returned item is still canonical chunk evidence from an active revision.
func expandRelationsFromEvidence(ctx context.Context, seeds []Evidence, authorizedKBIDs []int64, options RelationExpansionOptions) ([]Evidence, []RelationPath, error) {
	options = normalizeRelationExpansionOptions(options)
	if len(seeds) == 0 || len(authorizedKBIDs) == 0 || options.Depth <= 0 || options.EvidenceBudget <= 0 {
		return []Evidence{}, []RelationPath{}, nil
	}

	authorizedIDs := normalizedInt64Set(authorizedKBIDs)
	authorized := make(map[int64]struct{}, len(authorizedIDs))
	for _, id := range authorizedIDs {
		authorized[id] = struct{}{}
	}

	seenChunks := make(map[int64]struct{}, len(seeds)+options.EvidenceBudget)
	frontier := make([]int64, 0, len(seeds))
	for _, seed := range seeds {
		if seed.RetrievalUnitID <= 0 {
			continue
		}
		seenChunks[seed.RetrievalUnitID] = struct{}{}
		if _, ok := authorized[seed.KnowledgeBaseID]; ok {
			frontier = append(frontier, seed.RetrievalUnitID)
		}
	}
	frontier = normalizedInt64Set(frontier)
	if len(frontier) == 0 {
		applogger.Warn("KB relation expansion skipped: no authorized seed chunks", "seed_count", len(seeds))
		return []Evidence{}, []RelationPath{}, nil
	}

	added := make([]Evidence, 0, options.EvidenceBudget)
	paths := make([]RelationPath, 0)
	visitedRelations := make(map[int64]struct{}, options.Depth*options.Fanout)
	stats := relationExpansionStats{}
	// addedChunksByRelation records which evidence chunks each relation
	// contributed; chunkIDByRowID correlates evidence rows with chunks. Both
	// feed the final output audit so relation paths can be dropped or
	// scrubbed when the evidence they reference is blocked.
	addedChunksByRelation := make(map[int64][]int64)
	chunkIDByRowID := make(map[int64]int64)

	for depth := 1; depth <= options.Depth && len(frontier) > 0 && len(added) < options.EvidenceBudget; depth++ {
		relationIDs, err := relationIDsForFrontier(ctx, frontier, authorizedIDs, options.Fanout)
		if err != nil {
			return nil, nil, err
		}
		if len(relationIDs) == 0 {
			applogger.Debug("KB relation expansion found no relation frontier", "depth", depth, "frontier_count", len(frontier))
			break
		}

		relations, err := loadTraversableRelations(ctx, relationIDs)
		if err != nil {
			return nil, nil, err
		}
		entityLabels, err := relationEntityLabels(ctx, relations)
		if err != nil {
			return nil, nil, err
		}
		evidenceByRelation, err := relationEvidenceRows(ctx, relationIDs, authorizedIDs)
		if err != nil {
			return nil, nil, err
		}

		frontierSet := int64Membership(frontier)
		nextFrontier := make([]int64, 0)
		for _, relation := range relations {
			if len(added) >= options.EvidenceBudget {
				break
			}
			if _, visited := visitedRelations[relation.ID]; visited {
				continue
			}
			visitedRelations[relation.ID] = struct{}{}
			stats.RelationsConsidered++
			if !relationScopeAuthorized(relation.ScopeKBIDsJSON, authorized) {
				stats.RelationsSkippedUnauthorized++
				applogger.Warn("KB relation expansion skipped unauthorized relation scope", "relation_id", relation.ID)
				continue
			}
			subjectLabel, subjectOK := entityLabels[relation.SubjectEntityID]
			objectLabel, objectOK := entityLabels[relation.ObjectEntityID]
			if !subjectOK || !objectOK {
				stats.RelationsSkippedInactiveEntity++
				applogger.Warn("KB relation expansion skipped relation with inactive/missing entity", "relation_id", relation.ID, "subject_entity_id", relation.SubjectEntityID, "object_entity_id", relation.ObjectEntityID)
				continue
			}

			rows := evidenceByRelation[relation.ID]
			if len(rows) == 0 {
				stats.RelationsSkippedNoEvidence++
				applogger.Warn("KB relation expansion skipped relation without authorized evidence", "relation_id", relation.ID)
				continue
			}
			path := RelationPath{
				RelationID:          relation.ID,
				Depth:               depth,
				SubjectEntityID:     relation.SubjectEntityID,
				SubjectLabel:        subjectLabel,
				Predicate:           relation.Predicate,
				ObjectEntityID:      relation.ObjectEntityID,
				ObjectLabel:         objectLabel,
				ApplicabilityNote:   relation.ApplicabilityNote,
				SourceChunkIDs:      relationSourceChunks(rows, frontierSet),
				EvidenceChunkIDs:    make([]int64, 0, len(rows)),
				RelationEvidenceIDs: make([]int64, 0, len(rows)),
			}
			addedForPath := false

			for _, row := range rows {
				if row.ChunkID <= 0 {
					applogger.Warn("KB relation expansion skipped relation evidence without chunk", "relation_id", row.RelationID, "relation_evidence_id", row.ID)
					continue
				}
				path.RelationEvidenceIDs = append(path.RelationEvidenceIDs, row.ID)
				path.EvidenceChunkIDs = append(path.EvidenceChunkIDs, row.ChunkID)
				chunkIDByRowID[row.ID] = row.ChunkID
				if _, seen := seenChunks[row.ChunkID]; seen {
					continue
				}
				item, err := evidenceFromRelationEvidence(ctx, row)
				if err != nil {
					stats.EvidenceSkippedStaleOrInvalid++
					applogger.Warn("KB relation expansion skipped invalid relation evidence", "relation_id", row.RelationID, "relation_evidence_id", row.ID, "chunk_id", row.ChunkID, "error", err)
					continue
				}
				seenChunks[row.ChunkID] = struct{}{}
				added = append(added, item)
				addedChunksByRelation[relation.ID] = append(addedChunksByRelation[relation.ID], item.RetrievalUnitID)
				addedForPath = true
				nextFrontier = append(nextFrontier, row.ChunkID)
				if len(added) >= options.EvidenceBudget {
					break
				}
			}
			path.EvidenceChunkIDs = normalizedInt64Set(path.EvidenceChunkIDs)
			path.RelationEvidenceIDs = normalizedInt64Set(path.RelationEvidenceIDs)
			if addedForPath && len(path.SourceChunkIDs) > 0 && len(path.EvidenceChunkIDs) > 0 {
				paths = append(paths, path)
			}
		}
		frontier = normalizedInt64Set(nextFrontier)
	}

	sortRelationPaths(paths)
	audit := auditRelationExpandedEvidence(ctx, added, authorized)
	added = audit.Verified
	stats.UnauthorizedLeakageBlocked = audit.UnauthorizedBlocked
	stats.StaleLeakageBlocked = audit.StaleBlocked
	// The output contract covers relation paths too: a blocked leak must not
	// resurface as path metadata (relation existence, entity labels,
	// predicates, source/evidence chunk IDs).
	paths, stats.RelationPathsDroppedByAudit = scrubRelationPathsAfterAudit(paths, addedChunksByRelation, chunkIDByRowID, audit.BlockedChunks)
	// Relation-expanded evidence must remain explainable by the surviving
	// relation-path provenance. If a path was dropped because its source or
	// contributed evidence failed audit, downstream evidence discovered only
	// through that path is removed too.
	added, stats.EvidenceDroppedByPathAudit = filterEvidenceBySurvivingRelationPaths(added, paths)
	// Usage telemetry counts only relations whose evidence actually survived
	// the audit and whose path stayed in the output.
	survivingRelationIDs := make([]int64, 0, len(paths))
	for _, path := range paths {
		survivingRelationIDs = append(survivingRelationIDs, path.RelationID)
	}
	markRelationsUsed(ctx, survivingRelationIDs)
	// Info level keeps the necessary metric points observable in production
	// even when debug logging is disabled. The two leakage counters are
	// tripwires: non-zero values mean a policy regression was blocked by the
	// final audit and must be investigated.
	applogger.Info("KB relation expansion completed",
		"seed_count", len(seeds),
		"added_evidence_count", len(added),
		"relation_path_count", len(paths),
		"depth", options.Depth,
		"fanout", options.Fanout,
		"evidence_budget", options.EvidenceBudget,
		"relations_considered", stats.RelationsConsidered,
		"relations_skipped_unauthorized", stats.RelationsSkippedUnauthorized,
		"relations_skipped_inactive_entity", stats.RelationsSkippedInactiveEntity,
		"relations_skipped_no_authorized_evidence", stats.RelationsSkippedNoEvidence,
		"evidence_skipped_stale_or_invalid", stats.EvidenceSkippedStaleOrInvalid,
		"unauthorized_leakage_count", stats.UnauthorizedLeakageBlocked,
		"stale_leakage_count", stats.StaleLeakageBlocked,
		"relation_paths_dropped_by_audit", stats.RelationPathsDroppedByAudit,
		"evidence_dropped_by_path_audit", stats.EvidenceDroppedByPathAudit,
	)
	return added, paths, nil
}

// relationEvidenceAudit is the outcome of the final output-contract audit.
// BlockedChunks carries the chunk IDs removed from the output so relation
// paths referencing them can be dropped or scrubbed as well.
type relationEvidenceAudit struct {
	Verified            []Evidence
	BlockedChunks       map[int64]struct{}
	UnauthorizedBlocked int
	StaleBlocked        int
}

// auditRelationExpandedEvidence is the final output-contract audit. Every
// relation-expanded item must belong to a caller-authorized KB (checked
// in-memory, so this guarantee has no failure mode) and point at its
// document's active revision (re-checked in one batch query; each item was
// already grounding-validated at construction, so a query failure only drops
// the extra tripwire, never the guarantee). Detected leaks are dropped,
// counted and logged at Error level so they can never reach scan_kb output.
func auditRelationExpandedEvidence(ctx context.Context, added []Evidence, authorized map[int64]struct{}) relationEvidenceAudit {
	audit := relationEvidenceAudit{Verified: added, BlockedChunks: map[int64]struct{}{}}
	if len(added) == 0 {
		return audit
	}

	audited := make([]Evidence, 0, len(added))
	for _, item := range added {
		if _, ok := authorized[item.KnowledgeBaseID]; !ok {
			audit.UnauthorizedBlocked++
			audit.BlockedChunks[item.RetrievalUnitID] = struct{}{}
			applogger.Error("KB relation expansion final audit blocked unauthorized evidence", "chunk_id", item.RetrievalUnitID, "kb_id", item.KnowledgeBaseID, "document_id", item.DocumentID)
			continue
		}
		audited = append(audited, item)
	}

	documentIDs := make([]int64, 0, len(audited))
	seenDocuments := make(map[int64]struct{}, len(audited))
	for _, item := range audited {
		if _, exists := seenDocuments[item.DocumentID]; exists {
			continue
		}
		seenDocuments[item.DocumentID] = struct{}{}
		documentIDs = append(documentIDs, item.DocumentID)
	}
	var documents []model.Document
	if err := database.DB.WithContext(ctx).Select("id, status, active_revision_id").
		Where("id IN ?", documentIDs).Find(&documents).Error; err != nil {
		applogger.Error("KB relation expansion final audit could not re-verify active revisions; keeping construction-time grounding validation", "document_count", len(documentIDs), "error", err)
		audit.Verified = audited
		return audit
	}
	documentByID := make(map[int64]model.Document, len(documents))
	for _, document := range documents {
		documentByID[document.ID] = document
	}

	verified := make([]Evidence, 0, len(audited))
	for _, item := range audited {
		document, exists := documentByID[item.DocumentID]
		if !exists || document.Status != model.DocumentStatusReady || document.ActiveRevisionID != item.RevisionID {
			audit.StaleBlocked++
			audit.BlockedChunks[item.RetrievalUnitID] = struct{}{}
			applogger.Error("KB relation expansion final audit blocked non-active-revision evidence", "chunk_id", item.RetrievalUnitID, "document_id", item.DocumentID, "revision_id", item.RevisionID)
			continue
		}
		verified = append(verified, item)
	}
	audit.Verified = verified
	return audit
}

// scrubRelationPathsAfterAudit enforces the output contract on relation-path
// provenance after the final evidence audit:
//
//   - a path is dropped when none of the evidence chunks its relation
//     contributed survived the audit (a dangling path would leak the blocked
//     relation's existence, entity labels and predicates);
//   - a path is dropped when all of its source chunks were blocked (its
//     provenance anchor is gone);
//   - surviving paths have blocked chunk IDs and evidence-row IDs removed
//     from their metadata lists.
//
// The function only allocates when something was actually blocked; in normal
// operation it is a no-op returning the input unchanged.
func scrubRelationPathsAfterAudit(paths []RelationPath, addedChunksByRelation map[int64][]int64, chunkIDByRowID map[int64]int64, blockedChunks map[int64]struct{}) ([]RelationPath, int) {
	if len(paths) == 0 || len(blockedChunks) == 0 {
		return paths, 0
	}
	kept := make([]RelationPath, 0, len(paths))
	dropped := 0
	for _, path := range paths {
		contributedSurviving := false
		for _, chunkID := range addedChunksByRelation[path.RelationID] {
			if _, blocked := blockedChunks[chunkID]; !blocked {
				contributedSurviving = true
				break
			}
		}
		sourceChunkIDs := make([]int64, 0, len(path.SourceChunkIDs))
		for _, chunkID := range path.SourceChunkIDs {
			if _, blocked := blockedChunks[chunkID]; blocked {
				continue
			}
			sourceChunkIDs = append(sourceChunkIDs, chunkID)
		}
		if !contributedSurviving || len(sourceChunkIDs) == 0 {
			dropped++
			applogger.Error("KB relation expansion final audit dropped relation path", "relation_id", path.RelationID, "depth", path.Depth, "had_surviving_evidence", contributedSurviving)
			continue
		}
		evidenceChunkIDs := make([]int64, 0, len(path.EvidenceChunkIDs))
		for _, chunkID := range path.EvidenceChunkIDs {
			if _, blocked := blockedChunks[chunkID]; blocked {
				continue
			}
			evidenceChunkIDs = append(evidenceChunkIDs, chunkID)
		}
		relationEvidenceIDs := make([]int64, 0, len(path.RelationEvidenceIDs))
		for _, rowID := range path.RelationEvidenceIDs {
			if chunkID, known := chunkIDByRowID[rowID]; known {
				if _, blocked := blockedChunks[chunkID]; blocked {
					continue
				}
			}
			relationEvidenceIDs = append(relationEvidenceIDs, rowID)
		}
		path.SourceChunkIDs = sourceChunkIDs
		path.EvidenceChunkIDs = evidenceChunkIDs
		path.RelationEvidenceIDs = relationEvidenceIDs
		kept = append(kept, path)
	}
	return kept, dropped
}

// filterEvidenceBySurvivingRelationPaths keeps relation-expanded evidence
// aligned with its surviving provenance. Relation paths are not evidence, but
// every relation-expanded evidence item must still be explainable by at least
// one surviving path; otherwise the item came through a path that was later
// blocked or scrubbed by the final audit.
func filterEvidenceBySurvivingRelationPaths(added []Evidence, paths []RelationPath) ([]Evidence, int) {
	if len(added) == 0 {
		return added, 0
	}
	allowedChunks := make(map[int64]struct{})
	for _, path := range paths {
		for _, chunkID := range path.EvidenceChunkIDs {
			if chunkID > 0 {
				allowedChunks[chunkID] = struct{}{}
			}
		}
	}
	if len(allowedChunks) == 0 {
		for _, item := range added {
			applogger.Error("KB relation expansion final audit dropped evidence without surviving relation path", "chunk_id", item.RetrievalUnitID, "document_id", item.DocumentID, "revision_id", item.RevisionID)
		}
		return []Evidence{}, len(added)
	}
	kept := make([]Evidence, 0, len(added))
	dropped := 0
	for _, item := range added {
		if _, ok := allowedChunks[item.RetrievalUnitID]; !ok {
			dropped++
			applogger.Error("KB relation expansion final audit dropped evidence not covered by surviving relation paths", "chunk_id", item.RetrievalUnitID, "document_id", item.DocumentID, "revision_id", item.RevisionID)
			continue
		}
		kept = append(kept, item)
	}
	return kept, dropped
}

// normalizeRelationExpansionOptions applies safe defaults while preserving
// caller-provided positive bounds.
func normalizeRelationExpansionOptions(options RelationExpansionOptions) RelationExpansionOptions {
	if options.Depth <= 0 {
		options.Depth = DefaultRelationExpansionDepth
	}
	if options.Fanout <= 0 {
		options.Fanout = DefaultRelationExpansionFanout
	}
	if options.EvidenceBudget <= 0 {
		options.EvidenceBudget = DefaultRelationExpansionEvidenceBudget
	}
	return options
}

// relationIDsForFrontier finds traversable relations grounded by the current
// frontier chunks and already scoped to authorized KB evidence.
func relationIDsForFrontier(ctx context.Context, frontier []int64, authorizedKBIDs []int64, fanout int) ([]int64, error) {
	if len(frontier) == 0 || len(authorizedKBIDs) == 0 {
		return []int64{}, nil
	}
	states := []model.KBRelationState{model.KBRelationStateGrounded, model.KBRelationStateActive}
	var ids []int64
	err := database.DB.WithContext(ctx).Table("kb_relation_evidence").
		Select("DISTINCT kb_relation_evidence.relation_id").
		Joins("JOIN kb_relations ON kb_relations.id = kb_relation_evidence.relation_id").
		Where("kb_relation_evidence.chunk_id IN ? AND kb_relation_evidence.knowledge_base_id IN ? AND kb_relations.state IN ?", frontier, authorizedKBIDs, states).
		Order("kb_relation_evidence.relation_id ASC").
		Limit(fanout).
		Pluck("kb_relation_evidence.relation_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("load relation frontier: %w", err)
	}
	return normalizedInt64Set(ids), nil
}

// loadTraversableRelations loads only relation states that can participate in
// retrieval expansion. Stale, rejected and archived relations are excluded.
func loadTraversableRelations(ctx context.Context, relationIDs []int64) ([]model.KBRelation, error) {
	if len(relationIDs) == 0 {
		return []model.KBRelation{}, nil
	}
	states := []model.KBRelationState{model.KBRelationStateGrounded, model.KBRelationStateActive}
	var relations []model.KBRelation
	if err := database.DB.WithContext(ctx).
		Where("id IN ? AND state IN ?", relationIDs, states).
		Order("id ASC").
		Find(&relations).Error; err != nil {
		return nil, fmt.Errorf("load traversable relations: %w", err)
	}
	return relations, nil
}

// relationEntityLabels resolves active entity display labels for relation-path
// provenance. Missing or inactive entities make the relation unusable.
func relationEntityLabels(ctx context.Context, relations []model.KBRelation) (map[int64]string, error) {
	entityIDs := make([]int64, 0, len(relations)*2)
	for _, relation := range relations {
		entityIDs = append(entityIDs, relation.SubjectEntityID, relation.ObjectEntityID)
	}
	entityIDs = normalizedInt64Set(entityIDs)
	labels := make(map[int64]string, len(entityIDs))
	if len(entityIDs) == 0 {
		return labels, nil
	}
	var entities []model.KBEntity
	if err := database.DB.WithContext(ctx).
		Where("id IN ? AND state = ?", entityIDs, model.KBEntityStateActive).
		Find(&entities).Error; err != nil {
		return nil, fmt.Errorf("load relation entities: %w", err)
	}
	for _, entity := range entities {
		label := entity.DisplayLabel
		if label == "" {
			label = entity.NormalizedLabel
		}
		labels[entity.ID] = label
	}
	return labels, nil
}

// relationEvidenceRows loads authorized evidence rows for the selected
// relations. Evidence from unauthorized KBs is never returned to expansion.
func relationEvidenceRows(ctx context.Context, relationIDs []int64, authorizedKBIDs []int64) (map[int64][]model.KBRelationEvidence, error) {
	rowsByRelation := make(map[int64][]model.KBRelationEvidence, len(relationIDs))
	if len(relationIDs) == 0 || len(authorizedKBIDs) == 0 {
		return rowsByRelation, nil
	}
	var rows []model.KBRelationEvidence
	if err := database.DB.WithContext(ctx).
		Where("relation_id IN ? AND knowledge_base_id IN ?", relationIDs, authorizedKBIDs).
		Order("relation_id ASC, id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load relation evidence rows: %w", err)
	}
	for _, row := range rows {
		rowsByRelation[row.RelationID] = append(rowsByRelation[row.RelationID], row)
	}
	return rowsByRelation, nil
}

// relationScopeAuthorized requires the relation's entire KB scope to be
// included in the caller-authorized KB set, preventing cross-scope leakage.
func relationScopeAuthorized(scopeJSON string, authorized map[int64]struct{}) bool {
	var scope []int64
	if err := json.Unmarshal([]byte(scopeJSON), &scope); err != nil {
		applogger.Error("KB relation expansion failed to parse relation scope", "scope_json", scopeJSON, "error", err)
		return false
	}
	if len(scope) == 0 {
		return false
	}
	for _, kbID := range scope {
		if _, ok := authorized[kbID]; !ok {
			return false
		}
	}
	return true
}

// relationSourceChunks records which frontier chunks caused a relation path to
// be traversed. It is provenance metadata, not evidence content.
func relationSourceChunks(rows []model.KBRelationEvidence, frontier map[int64]struct{}) []int64 {
	source := make([]int64, 0)
	for _, row := range rows {
		if _, ok := frontier[row.ChunkID]; ok {
			source = append(source, row.ChunkID)
		}
	}
	return normalizedInt64Set(source)
}

// evidenceFromRelationEvidence converts one grounded relation evidence row
// back into canonical active chunk evidence for scan_kb.
func evidenceFromRelationEvidence(ctx context.Context, row model.KBRelationEvidence) (Evidence, error) {
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
		return Evidence{}, err
	}

	var document model.Document
	if err := database.DB.WithContext(ctx).Select("id, knowledge_base_id, title, status, active_revision_id").
		Where("id = ?", row.DocumentID).First(&document).Error; err != nil {
		return Evidence{}, fmt.Errorf("load relation evidence document: %w", err)
	}
	var chunk model.DocumentChunk
	if err := database.DB.WithContext(ctx).
		Where("id = ? AND knowledge_base_id = ? AND document_id = ? AND revision_id = ? AND deleted = 0", row.ChunkID, row.KnowledgeBaseID, row.DocumentID, row.RevisionID).
		First(&chunk).Error; err != nil {
		return Evidence{}, fmt.Errorf("load relation evidence chunk: %w", err)
	}
	content := chunk.DisplayText
	if content == "" {
		content = chunk.Content
	}
	locator := row.LocatorJSON
	if !hasCompleteLocator(locator) {
		resolvedLocator, err := locatorForChunk(row.ChunkID)
		if err != nil {
			return Evidence{}, fmt.Errorf("resolve relation-expanded locator for chunk %d: %w", row.ChunkID, err)
		}
		locator = resolvedLocator
	}
	if !hasCompleteLocator(locator) {
		return Evidence{}, fmt.Errorf("relation-expanded chunk %d has incomplete locator", row.ChunkID)
	}
	return Evidence{
		KnowledgeBaseID: row.KnowledgeBaseID,
		DocumentID:      row.DocumentID,
		DocumentTitle:   document.Title,
		RevisionID:      row.RevisionID,
		RetrievalUnitID: row.ChunkID,
		Content:         content,
		LocatorJSON:     locator,
		Score:           0,
		ExpansionKind:   EvidenceExpansionKindRelation,
	}, nil
}

// markRelationsUsed records lightweight utility telemetry for relation
// maintenance. Failure is logged but never blocks evidence retrieval.
func markRelationsUsed(ctx context.Context, relationIDs []int64) {
	if len(relationIDs) == 0 {
		return
	}
	err := database.DB.WithContext(ctx).Model(&model.KBRelation{}).
		Where("id IN ?", relationIDs).
		Updates(map[string]interface{}{
			"use_count":         gorm.Expr("use_count + ?", 1),
			"last_used_at_unix": time.Now().Unix(),
		}).Error
	if err != nil {
		applogger.Error("KB relation expansion failed to mark relations used", "relation_ids", relationIDs, "error", err)
	}
}

// int64Membership builds a positive-ID set for deterministic relation frontier
// checks.
func int64Membership(values []int64) map[int64]struct{} {
	set := make(map[int64]struct{}, len(values))
	for _, value := range values {
		if value > 0 {
			set[value] = struct{}{}
		}
	}
	return set
}

// sortRelationPaths makes relation provenance output stable for tests, logs
// and agent consumption.
func sortRelationPaths(paths []RelationPath) {
	sort.Slice(paths, func(i, j int) bool {
		if paths[i].Depth != paths[j].Depth {
			return paths[i].Depth < paths[j].Depth
		}
		return paths[i].RelationID < paths[j].RelationID
	})
}
