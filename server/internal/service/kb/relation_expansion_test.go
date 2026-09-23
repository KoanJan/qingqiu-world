package kb

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestRelationExpansionOutputsApplicabilityNoteAndSkipsNonTraversableRelations(t *testing.T) {
	db := openRelationExpansionTestDB(t)
	const (
		kbID         int64 = 1
		otherKBID    int64 = 2
		docID        int64 = 10
		revisionID   int64 = 20
		seedChunkID  int64 = 100
		nextChunkID  int64 = 101
		staleChunkID int64 = 102
	)
	createRelationExpansionFixture(t, db, kbID, docID, revisionID, seedChunkID, nextChunkID, staleChunkID)

	added, paths, err := expandRelationsFromEvidence(context.Background(), []Evidence{{
		KnowledgeBaseID: kbID,
		DocumentID:      docID,
		RevisionID:      revisionID,
		RetrievalUnitID: seedChunkID,
	}}, []int64{kbID}, RelationExpansionOptions{Depth: 1, Fanout: 10, EvidenceBudget: 10})
	if err != nil {
		t.Fatalf("expand relation evidence: %v", err)
	}
	if len(added) != 1 || added[0].RetrievalUnitID != nextChunkID {
		t.Fatalf("expected only grounded related chunk %d, got %#v", nextChunkID, added)
	}
	if len(paths) != 1 {
		t.Fatalf("expected one relation path, got %#v", paths)
	}
	if paths[0].ApplicabilityNote != "Supported only for the 2010-2013 cooperation period." {
		t.Fatalf("relation path should carry applicability note, got %q", paths[0].ApplicabilityNote)
	}
	if len(paths[0].SourceChunkIDs) != 1 || paths[0].SourceChunkIDs[0] != seedChunkID {
		t.Fatalf("relation path should record the source chunk, got %#v", paths[0].SourceChunkIDs)
	}

	added, paths, err = expandRelationsFromEvidence(context.Background(), []Evidence{{
		KnowledgeBaseID: kbID,
		DocumentID:      docID,
		RevisionID:      revisionID,
		RetrievalUnitID: seedChunkID,
	}}, []int64{otherKBID}, RelationExpansionOptions{Depth: 1, Fanout: 10, EvidenceBudget: 10})
	if err != nil {
		t.Fatalf("expand relation evidence with unauthorized scope: %v", err)
	}
	if len(added) != 0 || len(paths) != 0 {
		t.Fatalf("unauthorized expansion must not leak evidence or paths: added=%#v paths=%#v", added, paths)
	}
}

// TestFinalAuditDropsAndScrubsRelationPaths proves the output contract covers
// relation paths, not only evidence: when the final audit blocks leaked
// evidence, the corresponding path metadata is dropped or scrubbed so a
// blocked leak cannot resurface as provenance (relation existence, entity
// labels, predicates, source/evidence chunk IDs).
func TestFinalAuditDropsAndScrubsRelationPaths(t *testing.T) {
	db := openRelationExpansionTestDB(t)
	const (
		kbID int64 = 1
		// Document 10 keeps revision 20 active; document 12 points at another
		// revision so its evidence fails the active-revision re-check.
		activeDocID int64 = 10
		activeRevID int64 = 20
		staleDocID  int64 = 12
		staleRevID  int64 = 22
	)
	mustCreate(t, db, &model.Document{ID: activeDocID, KnowledgeBaseID: kbID, Title: "active doc", Status: model.DocumentStatusReady, ActiveRevisionID: activeRevID})
	mustCreate(t, db, &model.Document{ID: staleDocID, KnowledgeBaseID: kbID, Title: "replaced doc", Status: model.DocumentStatusReady, ActiveRevisionID: 99})

	authorized := map[int64]struct{}{kbID: {}}
	added := []Evidence{
		// Valid evidence contributed by relation 501.
		{KnowledgeBaseID: kbID, DocumentID: activeDocID, RevisionID: activeRevID, RetrievalUnitID: 201},
		// Unauthorized-KB evidence contributed by relation 502.
		{KnowledgeBaseID: 2, DocumentID: 11, RevisionID: 21, RetrievalUnitID: 210},
		// Non-active-revision evidence contributed by relation 503.
		{KnowledgeBaseID: kbID, DocumentID: staleDocID, RevisionID: staleRevID, RetrievalUnitID: 220},
		// Valid evidence also contributed by relation 503 (partial survival).
		{KnowledgeBaseID: kbID, DocumentID: activeDocID, RevisionID: activeRevID, RetrievalUnitID: 230},
		// Valid evidence contributed by relation 504, whose only source chunk
		// is the blocked unauthorized chunk 210.
		{KnowledgeBaseID: kbID, DocumentID: activeDocID, RevisionID: activeRevID, RetrievalUnitID: 240},
	}
	addedChunksByRelation := map[int64][]int64{
		501: {201},
		502: {210},
		503: {220, 230},
		504: {240},
	}
	chunkIDByRowID := map[int64]int64{
		601: 201, // relation 501 rows
		602: 210, // relation 502 rows
		603: 220, // relation 503 rows
		604: 230,
		608: 201,
		606: 210, // relation 504 rows (210 doubles as its frontier source)
		607: 240,
	}
	paths := []RelationPath{
		{RelationID: 501, Depth: 1, SubjectLabel: "Company A", Predicate: "relevant", ObjectLabel: "Company B", SourceChunkIDs: []int64{100}, EvidenceChunkIDs: []int64{201}, RelationEvidenceIDs: []int64{601}},
		{RelationID: 502, Depth: 1, SubjectLabel: "Company A", Predicate: "unauthorized", ObjectLabel: "Company B", SourceChunkIDs: []int64{100}, EvidenceChunkIDs: []int64{210}, RelationEvidenceIDs: []int64{602}},
		{RelationID: 503, Depth: 2, SubjectLabel: "Company A", Predicate: "partially_valid", ObjectLabel: "Company B", SourceChunkIDs: []int64{201}, EvidenceChunkIDs: []int64{201, 220, 230}, RelationEvidenceIDs: []int64{603, 604, 608}},
		{RelationID: 504, Depth: 2, SubjectLabel: "Company A", Predicate: "blocked_source", ObjectLabel: "Company B", SourceChunkIDs: []int64{210}, EvidenceChunkIDs: []int64{210, 240}, RelationEvidenceIDs: []int64{606, 607}},
	}

	audit := auditRelationExpandedEvidence(context.Background(), added, authorized)
	if audit.UnauthorizedBlocked != 1 || audit.StaleBlocked != 1 {
		t.Fatalf("expected one unauthorized and one stale block, got unauthorized=%d stale=%d", audit.UnauthorizedBlocked, audit.StaleBlocked)
	}
	if len(audit.Verified) != 3 {
		t.Fatalf("expected 3 verified evidence items, got %d", len(audit.Verified))
	}
	for _, item := range audit.Verified {
		if item.RetrievalUnitID == 210 || item.RetrievalUnitID == 220 {
			t.Fatalf("blocked chunk %d must not survive the audit", item.RetrievalUnitID)
		}
	}

	kept, dropped := scrubRelationPathsAfterAudit(paths, addedChunksByRelation, chunkIDByRowID, audit.BlockedChunks)
	if dropped != 2 {
		t.Fatalf("expected 2 dropped relation paths, got %d: %#v", dropped, kept)
	}
	if len(kept) != 2 || kept[0].RelationID != 501 || kept[1].RelationID != 503 {
		t.Fatalf("expected paths of relations 501 and 503 to survive, got %#v", kept)
	}
	filteredEvidence, evidenceDropped := filterEvidenceBySurvivingRelationPaths(audit.Verified, kept)
	if evidenceDropped != 1 {
		t.Fatalf("expected one evidence item without surviving path to be dropped, got %d: %#v", evidenceDropped, filteredEvidence)
	}
	for _, item := range filteredEvidence {
		if item.RetrievalUnitID == 240 {
			t.Fatalf("chunk 240 was discovered only through a dropped path and must not survive: %#v", filteredEvidence)
		}
	}
	if len(filteredEvidence) != 2 {
		t.Fatalf("expected only chunks covered by surviving relation paths, got %#v", filteredEvidence)
	}
	partial := kept[1]
	if len(partial.SourceChunkIDs) != 1 || partial.SourceChunkIDs[0] != 201 {
		t.Fatalf("surviving path must keep its unblocked source chunk, got %#v", partial.SourceChunkIDs)
	}
	for _, chunkID := range partial.EvidenceChunkIDs {
		if chunkID == 220 {
			t.Fatalf("blocked chunk 220 must be scrubbed from EvidenceChunkIDs, got %#v", partial.EvidenceChunkIDs)
		}
	}
	if len(partial.EvidenceChunkIDs) != 2 {
		t.Fatalf("expected scrubbed path to keep evidence chunks [201 230], got %#v", partial.EvidenceChunkIDs)
	}
	for _, rowID := range partial.RelationEvidenceIDs {
		if rowID == 603 {
			t.Fatalf("evidence row 603 of blocked chunk 220 must be scrubbed, got %#v", partial.RelationEvidenceIDs)
		}
	}

	// Without any blocked chunk the scrub step must be a no-op.
	samePaths, sameDropped := scrubRelationPathsAfterAudit(paths, addedChunksByRelation, chunkIDByRowID, nil)
	if sameDropped != 0 || len(samePaths) != len(paths) {
		t.Fatalf("scrub must be a no-op without blocked chunks, got dropped=%d paths=%d", sameDropped, len(samePaths))
	}
}

func openRelationExpansionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	oldDB := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "relation-expansion.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	database.DB = db
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		database.DB = oldDB
	})
	if err := db.AutoMigrate(
		&model.KnowledgeBase{},
		&model.Document{},
		&model.DocumentRevision{},
		&model.ContentNode{},
		&model.DocumentChunk{},
		&model.DocumentChunkNode{},
		&model.KBEntity{},
		&model.KBRelation{},
		&model.KBRelationEvidence{},
		&model.KBRelationJob{},
	); err != nil {
		t.Fatalf("auto-migrate relation expansion schema: %v", err)
	}
	return db
}

func createRelationExpansionFixture(t *testing.T, db *gorm.DB, kbID, docID, revisionID, seedChunkID, nextChunkID, staleChunkID int64) {
	t.Helper()
	mustCreate(t, db, &model.KnowledgeBase{ID: kbID, Name: "kb"})
	mustCreate(t, db, &model.Document{ID: docID, KnowledgeBaseID: kbID, Title: "doc", Status: model.DocumentStatusReady, ActiveRevisionID: revisionID})
	mustCreate(t, db, &model.DocumentRevision{ID: revisionID, DocumentID: docID, ContentHash: "revision-hash", Status: model.DocumentRevisionStatusReady})
	mustCreate(t, db, &model.ContentNode{ID: 201, DocumentID: docID, RevisionID: revisionID, Text: "Company A cooperated with Company B from 2010 to 2013.", ContentHash: "hash-seed", LocatorJSON: relationExpansionTestLocator(0)})
	mustCreate(t, db, &model.ContentNode{ID: 202, DocumentID: docID, RevisionID: revisionID, Text: "Company B published the joint project report.", ContentHash: "hash-next", LocatorJSON: relationExpansionTestLocator(1)})
	mustCreate(t, db, &model.ContentNode{ID: 203, DocumentID: docID, RevisionID: revisionID, Text: "Stale relation evidence should not appear.", ContentHash: "hash-stale", LocatorJSON: relationExpansionTestLocator(2)})
	mustCreate(t, db, &model.DocumentChunk{ID: seedChunkID, KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, ChunkIndex: 0, Content: "Company A cooperated with Company B from 2010 to 2013.", DisplayText: "Company A cooperated with Company B from 2010 to 2013."})
	mustCreate(t, db, &model.DocumentChunk{ID: nextChunkID, KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, ChunkIndex: 1, Content: "Company B published the joint project report.", DisplayText: "Company B published the joint project report."})
	mustCreate(t, db, &model.DocumentChunk{ID: staleChunkID, KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, ChunkIndex: 2, Content: "Stale relation evidence should not appear.", DisplayText: "Stale relation evidence should not appear."})
	mustCreate(t, db, &model.DocumentChunkNode{ChunkID: seedChunkID, NodeID: 201})
	mustCreate(t, db, &model.DocumentChunkNode{ChunkID: nextChunkID, NodeID: 202})
	mustCreate(t, db, &model.DocumentChunkNode{ChunkID: staleChunkID, NodeID: 203})
	mustCreate(t, db, &model.KBEntity{ID: 301, ScopeHash: "scope-1", ScopeKBIDsJSON: "[1]", NormalizedLabel: "company a", DisplayLabel: "Company A", State: model.KBEntityStateActive})
	mustCreate(t, db, &model.KBEntity{ID: 302, ScopeHash: "scope-1", ScopeKBIDsJSON: "[1]", NormalizedLabel: "company b", DisplayLabel: "Company B", State: model.KBEntityStateActive})
	mustCreate(t, db, &model.KBRelation{ID: 401, ScopeHash: "scope-1", ScopeKBIDsJSON: "[1]", SubjectEntityID: 301, Predicate: "cooperated_with", ObjectEntityID: 302, ApplicabilityNote: "Supported only for the 2010-2013 cooperation period.", State: model.KBRelationStateGrounded, IdempotencyKey: "grounded"})
	mustCreate(t, db, &model.KBRelationEvidence{RelationID: 401, KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, ContentNodeID: 201, ChunkID: seedChunkID, LocatorJSON: relationExpansionTestLocator(0), SupportQuote: "cooperated with Company B from 2010 to 2013", ContentHash: "hash-seed", EvidenceFingerprint: "grounded-seed"})
	mustCreate(t, db, &model.KBRelationEvidence{RelationID: 401, KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, ContentNodeID: 202, ChunkID: nextChunkID, LocatorJSON: relationExpansionTestLocator(1), SupportQuote: "published the joint project report", ContentHash: "hash-next", EvidenceFingerprint: "grounded-next"})
	mustCreate(t, db, &model.KBRelation{ID: 402, ScopeHash: "scope-1", ScopeKBIDsJSON: "[1]", SubjectEntityID: 301, Predicate: "stale_relation", ObjectEntityID: 302, State: model.KBRelationStateStale, IdempotencyKey: "stale"})
	mustCreate(t, db, &model.KBRelationEvidence{RelationID: 402, KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, ContentNodeID: 201, ChunkID: seedChunkID, LocatorJSON: relationExpansionTestLocator(0), SupportQuote: "cooperated with Company B from 2010 to 2013", ContentHash: "hash-seed", EvidenceFingerprint: "stale-seed"})
	mustCreate(t, db, &model.KBRelationEvidence{RelationID: 402, KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, ContentNodeID: 203, ChunkID: staleChunkID, LocatorJSON: relationExpansionTestLocator(2), SupportQuote: "Stale relation evidence should not appear", ContentHash: "hash-stale", EvidenceFingerprint: "stale-next"})
}

func relationExpansionTestLocator(chunkIndex int) string {
	return `{"source_kind":1,"file_type":"txt","chunk_index":` + strconv.Itoa(chunkIndex) + `,"char_start":0,"char_end":10,"line_start":1,"line_end":1,"page_start":0,"page_end":0}`
}

func mustCreate(t *testing.T, db *gorm.DB, value any) {
	t.Helper()
	if err := db.Create(value).Error; err != nil {
		t.Fatalf("create fixture %T: %v", value, err)
	}
}
