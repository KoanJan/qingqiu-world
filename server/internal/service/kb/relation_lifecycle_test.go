package kb

import (
	"context"
	"fmt"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

func TestRelationScopeHashIsStableForSameKBSet(t *testing.T) {
	left := RelationScopeHash([]int64{3, 1, 3, 2, 0})
	right := RelationScopeHash([]int64{2, 1, 3})
	if left != right {
		t.Fatalf("scope hash must ignore order, duplicates and invalid IDs: %q != %q", left, right)
	}
	if RelationScopeJSON([]int64{3, 1, 3, 2, 0}) != "[1,2,3]" {
		t.Fatalf("unexpected scope json: %s", RelationScopeJSON([]int64{3, 1, 3, 2, 0}))
	}
}

func TestRelationIdempotencyKeyNormalizesPredicate(t *testing.T) {
	scope := RelationScopeHash([]int64{7})
	left := RelationIdempotencyKey(scope, 11, " Maintained By ", 12, DefaultRelationPolicyVersion)
	right := RelationIdempotencyKey(scope, 11, "maintained_by", 12, DefaultRelationPolicyVersion)
	if left != right {
		t.Fatalf("predicate normalization should not change relation key: %q != %q", left, right)
	}
	if left == RelationIdempotencyKey(scope, 12, "maintained_by", 11, DefaultRelationPolicyVersion) {
		t.Fatal("subject/object direction must affect relation key")
	}
}

func TestNormalizeEntityLabel(t *testing.T) {
	got := NormalizeEntityLabel("  Service   A  ")
	if got != "service a" {
		t.Fatalf("unexpected normalized label: %q", got)
	}
}

func TestMergeApplicabilityNotesPreservesDistinctNaturalLanguageBoundaries(t *testing.T) {
	got := mergeApplicabilityNotes(
		"Supported only for the 2010-2013 cooperation period.",
		"Applies only to the Asia-Pacific joint venture.",
	)
	want := "Supported only for the 2010-2013 cooperation period.\nAdditional applicability: Applies only to the Asia-Pacific joint venture."
	if got != want {
		t.Fatalf("unexpected merged note:\nwant: %q\ngot:  %q", want, got)
	}
	if mergeApplicabilityNotes(got, "Supported only for the 2010-2013 cooperation period.") != got {
		t.Fatal("duplicate applicability note should not be appended")
	}
}

// createLifecycleFixture builds one grounded relation with two evidence chunks
// (a seed and an expansion-only chunk) and returns the seed Evidence used to
// drive expansion assertions.
func createLifecycleFixture(t *testing.T, relationID int64) (kbID, docID, revisionID, seedChunkID, nextChunkID int64, seed Evidence) {
	t.Helper()
	db := openRelationExpansionTestDB(t)
	const (
		subjectID int64 = 301
		objectID  int64 = 302
	)
	kbID, docID, revisionID, seedChunkID, nextChunkID = 1, 10, 20, 100, 101
	createRelationTestWorld(t, db, kbID, docID, revisionID, subjectID, objectID, "[1]")
	refSeed := createRelationEvidenceChunk(t, db, kbID, docID, revisionID, seedChunkID, "Company A anchor evidence.")
	refNext := createRelationEvidenceChunk(t, db, kbID, docID, revisionID, nextChunkID, "Company B expansion evidence.")
	createTestRelation(t, db,
		newRelationTestRelation(relationID, subjectID, objectID, "cooperated_with", model.KBRelationStateGrounded, "[1]"),
		[]relationEvidenceRef{refSeed, refNext},
	)
	return kbID, docID, revisionID, seedChunkID, nextChunkID, Evidence{KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, RetrievalUnitID: seedChunkID}
}

// assertRelationState fails unless the relation currently has the given state.
func assertRelationState(t *testing.T, relationID int64, want model.KBRelationState) {
	t.Helper()
	var relation model.KBRelation
	if err := testDB().First(&relation, relationID).Error; err != nil {
		t.Fatalf("load relation %d: %v", relationID, err)
	}
	if relation.State != want {
		t.Fatalf("relation %d state = %d, want %d", relationID, relation.State, want)
	}
}

// assertExpansionResult runs relation expansion from the seed and fails unless
// the added chunk IDs match exactly the expected set.
func assertExpansionResult(t *testing.T, seed Evidence, kbIDs []int64, wantChunkIDs ...int64) {
	t.Helper()
	added, paths, err := expandRelationsFromEvidence(context.Background(), []Evidence{seed}, kbIDs, RelationExpansionOptions{Depth: 2, Fanout: 10, EvidenceBudget: 10})
	if err != nil {
		t.Fatalf("expansion: %v", err)
	}
	got := make(map[int64]struct{}, len(added))
	for _, item := range added {
		got[item.RetrievalUnitID] = struct{}{}
	}
	want := make(map[int64]struct{}, len(wantChunkIDs))
	for _, id := range wantChunkIDs {
		want[id] = struct{}{}
	}
	if len(got) != len(want) {
		t.Fatalf("expansion added %d chunks %v, want %d %v", len(got), got, len(want), wantChunkIDs)
	}
	for id := range want {
		if _, exists := got[id]; !exists {
			t.Fatalf("expansion missing chunk %d, got %v", id, got)
		}
	}
	if len(got) == 0 && len(paths) != 0 {
		t.Fatalf("no evidence expansion must not emit relation paths: %#v", paths)
	}
}

// TestDocumentDeletionMarksRelationsStaleAndBlocksExpansion covers the
// document deletion path: every relation supported by the deleted document
// becomes stale, stops being traversable, and a bounded revalidation job is
// queued instead of a blind rescan.
func TestDocumentDeletionMarksRelationsStaleAndBlocksExpansion(t *testing.T) {
	const relationID int64 = 501
	kbID, docID, _, _, nextChunkID, seed := createLifecycleFixture(t, relationID)
	assertExpansionResult(t, seed, []int64{kbID}, nextChunkID)

	if err := MarkRelationsStaleForDocument(docID, "document deleted"); err != nil {
		t.Fatalf("mark document relations stale: %v", err)
	}
	assertRelationState(t, relationID, model.KBRelationStateStale)
	assertExpansionResult(t, seed, []int64{kbID})

	var jobCount int64
	if err := testDB().Model(&model.KBRelationJob{}).
		Where("relation_id = ? AND job_type = ?", relationID, model.KBRelationJobTypeRevalidateRelation).
		Count(&jobCount).Error; err != nil {
		t.Fatalf("count revalidation jobs: %v", err)
	}
	if jobCount != 1 {
		t.Fatalf("expected one queued revalidation job, got %d", jobCount)
	}
}

// TestRevisionReplacementMarksSupersededRelationsStale covers the stale
// revision scenario: activating a new document revision marks relations that
// cite older revisions stale, the old relation stops hitting expansion, and
// revalidation archives it because its evidence no longer points at the
// active revision.
func TestRevisionReplacementMarksSupersededRelationsStale(t *testing.T) {
	const (
		relationID    int64 = 501
		newRevisionID int64 = 21
	)
	kbID, docID, revisionID, _, nextChunkID, seed := createLifecycleFixture(t, relationID)
	assertExpansionResult(t, seed, []int64{kbID}, nextChunkID)

	db := testDB()
	mustCreate(t, db, &model.DocumentRevision{ID: newRevisionID, DocumentID: docID, ContentHash: "revision-hash-21", CanonicalFingerprint: fmt.Sprintf("canonical-%d-%d", docID, newRevisionID), Status: model.DocumentRevisionStatusReady})
	if err := db.Model(&model.Document{}).Where("id = ?", docID).
		Updates(map[string]interface{}{"active_revision_id": newRevisionID}).Error; err != nil {
		t.Fatalf("activate new revision: %v", err)
	}
	if err := MarkRelationsStaleForSupersededDocumentRevisions(docID, newRevisionID, "document active revision changed"); err != nil {
		t.Fatalf("mark superseded relations stale: %v", err)
	}

	assertRelationState(t, relationID, model.KBRelationStateStale)
	// The stale relation must not hit even though its evidence rows still
	// reference the old revision's chunks.
	assertExpansionResult(t, seed, []int64{kbID})

	if err := RevalidateStaleRelation(relationID); err != nil {
		t.Fatalf("revalidate stale relation: %v", err)
	}
	assertRelationState(t, relationID, model.KBRelationStateArchived)
	assertExpansionResult(t, seed, []int64{kbID})
	if revisionID == newRevisionID {
		t.Fatal("fixture sanity: revisions must differ")
	}
}

// TestRevalidateStaleRelationRestoresGroundedWhenEvidenceValid covers the
// recovery path: a stale relation whose evidence still points at the active
// revision returns to grounded and becomes traversable again.
func TestRevalidateStaleRelationRestoresGroundedWhenEvidenceValid(t *testing.T) {
	const relationID int64 = 501
	kbID, _, _, _, nextChunkID, seed := createLifecycleFixture(t, relationID)

	db := testDB()
	if err := db.Model(&model.KBRelation{}).Where("id = ?", relationID).
		Updates(map[string]interface{}{"state": model.KBRelationStateStale, "stale_reason": "simulated trigger"}).Error; err != nil {
		t.Fatalf("simulate stale relation: %v", err)
	}
	assertExpansionResult(t, seed, []int64{kbID})

	if err := RevalidateStaleRelation(relationID); err != nil {
		t.Fatalf("revalidate stale relation: %v", err)
	}
	assertRelationState(t, relationID, model.KBRelationStateGrounded)
	assertExpansionResult(t, seed, []int64{kbID}, nextChunkID)
}

// testDB returns the package-global test database handle.
func testDB() *gorm.DB {
	return database.DB
}
