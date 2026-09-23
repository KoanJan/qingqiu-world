package kb

import (
	"context"
	"fmt"
	"testing"

	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

// This file hosts the dedicated relation-selection test set for dense graphs
// and cycles, plus the shared fixture builders reused by the relation
// lifecycle and evaluation tests.

// relationEvidenceRef points at one canonical evidence chunk created for a
// relation fixture. Support quotes are intentionally empty here: quote
// grounding is admission behavior covered by the analyzer tests, while these
// tests exercise traversal and lifecycle policy.
type relationEvidenceRef struct {
	KnowledgeBaseID int64
	DocumentID      int64
	RevisionID      int64
	ChunkID         int64
	ContentNodeID   int64
}

// createRelationEvidenceChunk persists one canonical evidence chunk (content
// node, retrieval unit and chunk-node mapping) under an active document
// revision and returns the reference used to build relation evidence rows.
func createRelationEvidenceChunk(t *testing.T, db *gorm.DB, kbID, docID, revisionID, chunkID int64, text string) relationEvidenceRef {
	t.Helper()
	nodeID := chunkID + 100000
	contentHash := fmt.Sprintf("hash-%d", chunkID)
	locator := relationExpansionTestLocator(int(chunkID % 1000))
	mustCreate(t, db, &model.ContentNode{ID: nodeID, DocumentID: docID, RevisionID: revisionID, Text: text, ContentHash: contentHash, LocatorJSON: locator})
	mustCreate(t, db, &model.DocumentChunk{ID: chunkID, KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, ChunkIndex: int(chunkID % 1000), Content: text, DisplayText: text})
	mustCreate(t, db, &model.DocumentChunkNode{ChunkID: chunkID, NodeID: nodeID})
	return relationEvidenceRef{KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, ChunkID: chunkID, ContentNodeID: nodeID}
}

// createTestRelation persists one relation and its evidence rows. Evidence
// rows keep empty support quotes and carry the chunk's content hash so
// grounding validation still verifies the full document/revision/node chain.
func createTestRelation(t *testing.T, db *gorm.DB, relation model.KBRelation, refs []relationEvidenceRef) {
	t.Helper()
	mustCreate(t, db, &relation)
	for _, ref := range refs {
		row := model.KBRelationEvidence{
			RelationID:          relation.ID,
			KnowledgeBaseID:     ref.KnowledgeBaseID,
			DocumentID:          ref.DocumentID,
			RevisionID:          ref.RevisionID,
			ContentNodeID:       ref.ContentNodeID,
			ChunkID:             ref.ChunkID,
			LocatorJSON:         relationExpansionTestLocator(int(ref.ChunkID % 1000)),
			EvidenceFingerprint: fmt.Sprintf("rel-%d-%d", relation.ID, ref.ChunkID),
		}
		mustCreate(t, db, &row)
	}
}

// createRelationTestWorld persists the minimal KB/document/revision/entity
// scaffold shared by relation selection, lifecycle and evaluation fixtures.
func createRelationTestWorld(t *testing.T, db *gorm.DB, kbID, docID, revisionID, subjectEntityID, objectEntityID int64, scopeJSON string) {
	t.Helper()
	mustCreate(t, db, &model.KnowledgeBase{ID: kbID, Name: fmt.Sprintf("kb-%d", kbID)})
	mustCreate(t, db, &model.Document{ID: docID, KnowledgeBaseID: kbID, Title: fmt.Sprintf("doc-%d", docID), Status: model.DocumentStatusReady, ActiveRevisionID: revisionID})
	mustCreate(t, db, &model.DocumentRevision{ID: revisionID, DocumentID: docID, ContentHash: fmt.Sprintf("revision-hash-%d", revisionID), CanonicalFingerprint: fmt.Sprintf("canonical-%d-%d", docID, revisionID), Status: model.DocumentRevisionStatusReady})
	mustCreate(t, db, &model.KBEntity{ID: subjectEntityID, ScopeHash: "scope-1", ScopeKBIDsJSON: scopeJSON, NormalizedLabel: "company a", DisplayLabel: "Company A", State: model.KBEntityStateActive})
	mustCreate(t, db, &model.KBEntity{ID: objectEntityID, ScopeHash: "scope-1", ScopeKBIDsJSON: scopeJSON, NormalizedLabel: "company b", DisplayLabel: "Company B", State: model.KBEntityStateActive})
}

// newRelationTestRelation builds a traversable-relation fixture row.
func newRelationTestRelation(relationID, subjectEntityID, objectEntityID int64, predicate string, state model.KBRelationState, scopeJSON string) model.KBRelation {
	return model.KBRelation{
		ID:              relationID,
		ScopeHash:       "scope-1",
		ScopeKBIDsJSON:  scopeJSON,
		SubjectEntityID: subjectEntityID,
		Predicate:       predicate,
		ObjectEntityID:  objectEntityID,
		State:           state,
		IdempotencyKey:  fmt.Sprintf("relation-%d", relationID),
	}
}

// assertNoDuplicateChunkIDs fails the test when any chunk ID appears twice.
func assertNoDuplicateChunkIDs(t *testing.T, evidence []Evidence) {
	t.Helper()
	seen := make(map[int64]struct{}, len(evidence))
	for _, item := range evidence {
		if _, exists := seen[item.RetrievalUnitID]; exists {
			t.Fatalf("duplicate chunk ID in expansion result: %d", item.RetrievalUnitID)
		}
		seen[item.RetrievalUnitID] = struct{}{}
	}
}

// TestDenseRelationExpansionStaysBoundedAndDeterministic proves the selection
// policy keeps dense relation graphs from swamping scan_kb evidence: fanout
// bounds relation rows per frontier hop, the evidence budget caps total
// expansion, and selection uses the deterministic lowest-relation-ID
// tie-break.
func TestDenseRelationExpansionStaysBoundedAndDeterministic(t *testing.T) {
	db := openRelationExpansionTestDB(t)
	const (
		kbID               int64 = 1
		docID              int64 = 10
		revisionID         int64 = 20
		seedChunkID        int64 = 100
		subjectID          int64 = 301
		objectID           int64 = 302
		denseRelationCount       = 8
	)
	createRelationTestWorld(t, db, kbID, docID, revisionID, subjectID, objectID, "[1]")

	refs := map[int64]relationEvidenceRef{}
	refs[seedChunkID] = createRelationEvidenceChunk(t, db, kbID, docID, revisionID, seedChunkID, "Company A anchor evidence.")
	for i := 0; i < denseRelationCount; i++ {
		chunkID := int64(200 + i)
		refs[chunkID] = createRelationEvidenceChunk(t, db, kbID, docID, revisionID, chunkID, fmt.Sprintf("Dense relation evidence %d.", i))
		createTestRelation(t, db,
			newRelationTestRelation(int64(500+i), subjectID, objectID, fmt.Sprintf("dense_%d", i), model.KBRelationStateGrounded, "[1]"),
			[]relationEvidenceRef{refs[seedChunkID], refs[chunkID]},
		)
	}

	seeds := []Evidence{{KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, RetrievalUnitID: seedChunkID}}

	// Fanout bounds the relation rows considered from one frontier hop and the
	// tie-break picks the lowest relation IDs deterministically.
	added, paths, err := expandRelationsFromEvidence(context.Background(), seeds, []int64{kbID}, RelationExpansionOptions{Depth: 1, Fanout: 3, EvidenceBudget: 10})
	if err != nil {
		t.Fatalf("dense expansion: %v", err)
	}
	if len(added) != 3 || len(paths) != 3 {
		t.Fatalf("fanout=3 should bound expansion to 3 relations, got added=%d paths=%d", len(added), len(paths))
	}
	for i, wantRelationID := range []int64{500, 501, 502} {
		if paths[i].RelationID != wantRelationID {
			t.Fatalf("expected deterministic relation order, path %d = relation %d, want %d", i, paths[i].RelationID, wantRelationID)
		}
		if added[i].RetrievalUnitID != int64(200+i) {
			t.Fatalf("expected chunk %d from relation %d, got %d", 200+i, wantRelationID, added[i].RetrievalUnitID)
		}
	}
	assertNoDuplicateChunkIDs(t, added)

	// The evidence budget caps expansion even when fanout is wide: a dense
	// graph cannot swamp scan_kb anchors.
	added, paths, err = expandRelationsFromEvidence(context.Background(), seeds, []int64{kbID}, RelationExpansionOptions{Depth: 1, Fanout: 20, EvidenceBudget: 4})
	if err != nil {
		t.Fatalf("dense expansion with wide fanout: %v", err)
	}
	if len(added) != 4 || len(paths) != 4 {
		t.Fatalf("evidence budget=4 should cap expansion, got added=%d paths=%d", len(added), len(paths))
	}
	assertNoDuplicateChunkIDs(t, added)
}

// TestRelationExpansionTerminatesOnCyclicGraph proves the visited-relation and
// visited-evidence guards make A -> B -> C -> A cycles terminate without
// duplicates, and that a deeper budget does not change the result (stability).
func TestRelationExpansionTerminatesOnCyclicGraph(t *testing.T) {
	db := openRelationExpansionTestDB(t)
	const (
		kbID       int64 = 1
		docID      int64 = 10
		revisionID int64 = 20
		chunkA     int64 = 100
		chunkB     int64 = 101
		chunkC     int64 = 102
		subjectID  int64 = 301
		objectID   int64 = 302
	)
	createRelationTestWorld(t, db, kbID, docID, revisionID, subjectID, objectID, "[1]")

	refA := createRelationEvidenceChunk(t, db, kbID, docID, revisionID, chunkA, "Cycle node A.")
	refB := createRelationEvidenceChunk(t, db, kbID, docID, revisionID, chunkB, "Cycle node B.")
	refC := createRelationEvidenceChunk(t, db, kbID, docID, revisionID, chunkC, "Cycle node C.")
	// A -> B, B -> C, C -> A: the last edge closes the cycle on the seed.
	createTestRelation(t, db, newRelationTestRelation(500, subjectID, objectID, "cycle_a_to_b", model.KBRelationStateGrounded, "[1]"), []relationEvidenceRef{refA, refB})
	createTestRelation(t, db, newRelationTestRelation(501, subjectID, objectID, "cycle_b_to_c", model.KBRelationStateGrounded, "[1]"), []relationEvidenceRef{refB, refC})
	createTestRelation(t, db, newRelationTestRelation(502, subjectID, objectID, "cycle_c_to_a", model.KBRelationStateGrounded, "[1]"), []relationEvidenceRef{refC, refA})

	seeds := []Evidence{{KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, RetrievalUnitID: chunkA}}
	expandedByDepth := map[int][]Evidence{}
	for _, depth := range []int{1, 2, 6} {
		added, paths, err := expandRelationsFromEvidence(context.Background(), seeds, []int64{kbID}, RelationExpansionOptions{Depth: depth, Fanout: 10, EvidenceBudget: 10})
		if err != nil {
			t.Fatalf("cyclic expansion depth=%d: %v", depth, err)
		}
		assertNoDuplicateChunkIDs(t, added)
		seenRelations := make(map[int64]struct{}, len(paths))
		for _, path := range paths {
			if _, exists := seenRelations[path.RelationID]; exists {
				t.Fatalf("relation %d emitted more than one path at depth=%d", path.RelationID, depth)
			}
			seenRelations[path.RelationID] = struct{}{}
		}
		for _, item := range added {
			if item.RetrievalUnitID == chunkA {
				t.Fatalf("cycle must not re-add the seed chunk: %#v", added)
			}
		}
		if len(added) != 2 {
			t.Fatalf("cycle expansion should reach B and C only, depth=%d added=%#v", depth, added)
		}
		expandedByDepth[depth] = added
	}
	// A deeper traversal budget must not change the cyclic result.
	if len(expandedByDepth[1]) != len(expandedByDepth[6]) {
		t.Fatalf("cycle expansion must be depth-stable: depth1=%d depth6=%d", len(expandedByDepth[1]), len(expandedByDepth[6]))
	}
}
