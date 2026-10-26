package kb

import (
	"context"
	"fmt"
	"testing"

	"qingqiu-world-server/internal/model"
)

// relationEvaluationDatasetVersion identifies the fixed evaluation fixture.
// Every metric below is computed deterministically from this dataset; no LLM
// is involved in scoring (project rule: LLM scoring is forbidden).
const relationEvaluationDatasetVersion = "relation-eval-v1"

// relationEvaluationFixture is the fixed relation-expansion evaluation
// dataset:
//
//	KB 1 (caller-authorized), doc 10, active revision 20:
//	  chunk 100  anchor seed (stands in for the vector-retrieval hit)
//	  chunk 101  relevant evidence, reachable only via relation R1
//	  chunk 102  relevant evidence, reachable only via relation R2 at depth 2
//	  chunk 103  noise evidence, reachable via the off-topic relation R3
//	  chunk 104  forbidden evidence, referenced only by the stale relation R4
//	KB 2 (not authorized for the caller), doc 11, active revision 21:
//	  chunk 110  forbidden evidence; R5 has scope [2] with one evidence row in
//	             KB 1 (so it reaches the in-loop scope check) and one in KB 2
//
// Ground truth: relevant = {101, 102}, noise = {103}, forbidden = {104, 110}.
type relationEvaluationFixture struct {
	AuthorizedKBIDs     []int64
	Seed                Evidence
	RelevantChunkIDs    []int64
	NoiseChunkIDs       []int64
	StaleChunkIDs       []int64
	UnauthorizedKBID    int64
	UnauthorizedChunkID int64
	RelationIDs         map[string]int64
}

// buildRelationEvaluationDataset persists the fixture rows and returns the
// ground-truth handle set used by the metric assertions.
func buildRelationEvaluationDataset(t *testing.T) relationEvaluationFixture {
	t.Helper()
	db := openRelationExpansionTestDB(t)
	const (
		kbID       int64 = 1
		otherKBID  int64 = 2
		docID      int64 = 10
		revisionID int64 = 20
		subjectID  int64 = 301
		objectID   int64 = 302

		seedChunkID int64 = 100
		relevant1   int64 = 101
		relevant2   int64 = 102
		noiseChunk  int64 = 103
		staleChunk  int64 = 104

		otherDocID      int64 = 11
		otherRevisionID int64 = 21
		otherChunkID    int64 = 110
	)
	createRelationTestWorld(t, db, kbID, docID, revisionID, subjectID, objectID, "[1]")
	mustCreate(t, db, &model.KnowledgeBase{ID: otherKBID, Name: "unauthorized kb"})
	mustCreate(t, db, &model.Document{ID: otherDocID, KnowledgeBaseID: otherKBID, Title: "unauthorized doc", Status: model.DocumentStatusReady, ActiveRevisionID: otherRevisionID})
	mustCreate(t, db, &model.DocumentRevision{ID: otherRevisionID, DocumentID: otherDocID, ContentHash: "revision-hash-21", CanonicalFingerprint: fmt.Sprintf("canonical-%d-%d", otherDocID, otherRevisionID), Status: model.DocumentRevisionStatusReady})

	refSeed := createRelationEvidenceChunk(t, db, kbID, docID, revisionID, seedChunkID, "Company A anchor evidence.")
	refRelevant1 := createRelationEvidenceChunk(t, db, kbID, docID, revisionID, relevant1, "Company B relevant report.")
	refRelevant2 := createRelationEvidenceChunk(t, db, kbID, docID, revisionID, relevant2, "Company B follow-up relevant report.")
	refNoise := createRelationEvidenceChunk(t, db, kbID, docID, revisionID, noiseChunk, "Unrelated canteen menu.")
	refStale := createRelationEvidenceChunk(t, db, kbID, docID, revisionID, staleChunk, "Stale relation evidence that must never surface.")
	refUnauthorized := createRelationEvidenceChunk(t, db, otherKBID, otherDocID, otherRevisionID, otherChunkID, "Evidence of an unauthorized KB.")

	createTestRelation(t, db, newRelationTestRelation(501, subjectID, objectID, "relevant_a_to_b", model.KBRelationStateGrounded, "[1]"), []relationEvidenceRef{refSeed, refRelevant1})
	createTestRelation(t, db, newRelationTestRelation(502, subjectID, objectID, "relevant_b_to_c", model.KBRelationStateGrounded, "[1]"), []relationEvidenceRef{refRelevant1, refRelevant2})
	createTestRelation(t, db, newRelationTestRelation(503, subjectID, objectID, "noise_off_topic", model.KBRelationStateGrounded, "[1]"), []relationEvidenceRef{refSeed, refNoise})
	createTestRelation(t, db, newRelationTestRelation(504, subjectID, objectID, "stale_edge", model.KBRelationStateStale, "[1]"), []relationEvidenceRef{refSeed, refStale})
	// R5 reaches the in-loop scope check through its KB-1 evidence row but its
	// scope [2] is not a subset of the authorized KBs, so it must be skipped.
	createTestRelation(t, db, newRelationTestRelation(505, subjectID, objectID, "cross_kb_edge", model.KBRelationStateGrounded, "[2]"), []relationEvidenceRef{refSeed, refUnauthorized})

	return relationEvaluationFixture{
		AuthorizedKBIDs:     []int64{kbID},
		Seed:                Evidence{KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, RetrievalUnitID: seedChunkID},
		RelevantChunkIDs:    []int64{relevant1, relevant2},
		NoiseChunkIDs:       []int64{noiseChunk},
		StaleChunkIDs:       []int64{staleChunk},
		UnauthorizedKBID:    otherKBID,
		UnauthorizedChunkID: otherChunkID,
		RelationIDs:         map[string]int64{"relevant": 501, "relevant_depth2": 502, "noise": 503, "stale": 504, "unauthorized": 505},
	}
}

// runRelationEvaluationExpansion executes the enabled arm of the evaluation:
// anchors plus relation expansion, mirroring the ScanOptions.ExpandRelations
// branch of ScanMultiKBEvidence.
func runRelationEvaluationExpansion(t *testing.T, fixture relationEvaluationFixture) ([]Evidence, []RelationPath) {
	t.Helper()
	added, paths, err := expandRelationsFromEvidence(context.Background(), []Evidence{fixture.Seed}, fixture.AuthorizedKBIDs, RelationExpansionOptions{Depth: 2, Fanout: 10, EvidenceBudget: 10})
	if err != nil {
		t.Fatalf("evaluation expansion: %v", err)
	}
	return added, paths
}

// chunkIDSet converts evidence into a chunk-ID membership set.
func chunkIDSet(evidence []Evidence) map[int64]struct{} {
	set := make(map[int64]struct{}, len(evidence))
	for _, item := range evidence {
		set[item.RetrievalUnitID] = struct{}{}
	}
	return set
}

// containsAny reports whether any of the wanted IDs appears in the set.
func containsAny(set map[int64]struct{}, wanted []int64) bool {
	for _, id := range wanted {
		if _, exists := set[id]; exists {
			return true
		}
	}
	return false
}

// TestRelationEvaluationDeterministicMetrics computes the 0.1.15 evaluation
// metrics from the fixed dataset: relation precision, semantic expansion
// recall gain (enabled vs disabled), noise introduction, and the two leakage
// invariants (stale = 0, unauthorized = 0). All expectations are exact
// because the dataset and the traversal policy are deterministic.
func TestRelationEvaluationDeterministicMetrics(t *testing.T) {
	fixture := buildRelationEvaluationDataset(t)

	// Disabled arm (mirrors ScanOptions.ExpandRelations = false): the evidence
	// set is the anchor retrieval alone.
	disabledEvidence := []Evidence{fixture.Seed}
	disabledSet := chunkIDSet(disabledEvidence)
	disabledRecall := 0
	for _, id := range fixture.RelevantChunkIDs {
		if _, exists := disabledSet[id]; exists {
			disabledRecall++
		}
	}

	// Enabled arm: anchors plus relation expansion.
	added, paths := runRelationEvaluationExpansion(t, fixture)
	enabledSet := chunkIDSet(append(append([]Evidence{}, disabledEvidence...), added...))
	enabledRecall := 0
	for _, id := range fixture.RelevantChunkIDs {
		if _, exists := enabledSet[id]; exists {
			enabledRecall++
		}
	}

	// Semantic Expansion Recall Gain: expansion must strictly increase recall
	// of the ground-truth relevant evidence.
	recallGain := enabledRecall - disabledRecall
	t.Logf("relation evaluation %s: disabled_recall=%d/%d enabled_recall=%d/%d gain=+%d",
		relationEvaluationDatasetVersion, disabledRecall, len(fixture.RelevantChunkIDs), enabledRecall, len(fixture.RelevantChunkIDs), recallGain)
	if recallGain != len(fixture.RelevantChunkIDs) {
		t.Fatalf("semantic expansion recall gain = %d, want %d (expansion must reach all relevant evidence)", recallGain, len(fixture.RelevantChunkIDs))
	}

	// Relation Precision: relevant share of the relation-expanded evidence.
	relevantExpanded := 0
	for _, item := range added {
		for _, id := range fixture.RelevantChunkIDs {
			if item.RetrievalUnitID == id {
				relevantExpanded++
			}
		}
	}
	precision := float64(relevantExpanded) / float64(len(added))
	t.Logf("relation evaluation %s: precision=%d/%d=%.3f", relationEvaluationDatasetVersion, relevantExpanded, len(added), precision)
	if relevantExpanded != len(fixture.RelevantChunkIDs) || len(added) != 3 {
		t.Fatalf("relation precision regression: relevant=%d added=%d, want relevant=%d added=3 (one noise item)", relevantExpanded, len(added), len(fixture.RelevantChunkIDs))
	}

	// Noise Introduction: the off-topic relation contributes exactly one noise
	// item, and that item must still be canonical active evidence with full
	// provenance (noise never degrades the evidence contract).
	noiseIntroduced := 0
	for _, item := range added {
		for _, id := range fixture.NoiseChunkIDs {
			if item.RetrievalUnitID == id {
				noiseIntroduced++
				if item.KnowledgeBaseID != fixture.AuthorizedKBIDs[0] || item.RevisionID != fixture.Seed.RevisionID || item.LocatorJSON == "" || item.DocumentTitle == "" {
					t.Fatalf("noise evidence must still be canonical active evidence with complete provenance: %#v", item)
				}
			}
		}
	}
	t.Logf("relation evaluation %s: noise_introduced=%d", relationEvaluationDatasetVersion, noiseIntroduced)
	if noiseIntroduced != len(fixture.NoiseChunkIDs) {
		t.Fatalf("noise introduction = %d, want %d (bounded, deterministic noise)", noiseIntroduced, len(fixture.NoiseChunkIDs))
	}

	// Stale leakage = 0 and unauthorized leakage = 0: forbidden evidence and
	// unauthorized KB content must never appear in expansion output.
	expandedSet := chunkIDSet(added)
	if containsAny(expandedSet, fixture.StaleChunkIDs) {
		t.Fatalf("stale leakage: forbidden stale chunks %v appeared in expansion output", fixture.StaleChunkIDs)
	}
	if containsAny(expandedSet, []int64{fixture.UnauthorizedChunkID}) {
		t.Fatalf("unauthorized leakage: chunk %d of KB %d appeared in expansion output", fixture.UnauthorizedChunkID, fixture.UnauthorizedKBID)
	}
	for _, item := range added {
		if item.KnowledgeBaseID == fixture.UnauthorizedKBID {
			t.Fatalf("unauthorized leakage: evidence of KB %d appeared in expansion output", fixture.UnauthorizedKBID)
		}
	}
	for _, path := range paths {
		if path.RelationID == fixture.RelationIDs["unauthorized"] {
			t.Fatalf("unauthorized leakage: relation path of relation %d appeared in expansion output", fixture.RelationIDs["unauthorized"])
		}
		if path.RelationID == fixture.RelationIDs["stale"] {
			t.Fatalf("stale leakage: relation path of stale relation %d appeared in expansion output", fixture.RelationIDs["stale"])
		}
	}

	// Deterministic ordering: paths sorted by (depth, relation ID).
	expectedPathOrder := []int64{fixture.RelationIDs["relevant"], fixture.RelationIDs["noise"], fixture.RelationIDs["relevant_depth2"]}
	if len(paths) != len(expectedPathOrder) {
		t.Fatalf("expected %d relation paths, got %d: %#v", len(expectedPathOrder), len(paths), paths)
	}
	for i, wantRelationID := range expectedPathOrder {
		if paths[i].RelationID != wantRelationID {
			t.Fatalf("relation path %d = relation %d, want %d (deterministic (depth, relation_id) order)", i, paths[i].RelationID, wantRelationID)
		}
	}
}

// TestRelationEvaluationLifecycleCorrectness reruns the evaluation expansion
// after the stale-revision lifecycle event: activating a new document revision
// removes every previously traversable relation from expansion without
// leaking any old-revision evidence.
func TestRelationEvaluationLifecycleCorrectness(t *testing.T) {
	fixture := buildRelationEvaluationDataset(t)
	added, _ := runRelationEvaluationExpansion(t, fixture)
	if len(added) == 0 {
		t.Fatal("fixture sanity: expansion must produce evidence before the lifecycle event")
	}

	const newRevisionID int64 = 22
	docID := fixture.Seed.DocumentID
	db := testDB()
	mustCreate(t, db, &model.DocumentRevision{ID: newRevisionID, DocumentID: docID, ContentHash: "revision-hash-22", CanonicalFingerprint: fmt.Sprintf("canonical-%d-%d", docID, newRevisionID), Status: model.DocumentRevisionStatusReady})
	if err := db.Model(&model.Document{}).Where("id = ?", docID).
		Updates(map[string]interface{}{"active_revision_id": newRevisionID}).Error; err != nil {
		t.Fatalf("activate new revision: %v", err)
	}
	if err := MarkRelationsStaleForSupersededDocumentRevisions(docID, newRevisionID, "document active revision changed"); err != nil {
		t.Fatalf("mark superseded relations stale: %v", err)
	}

	added, paths := runRelationEvaluationExpansion(t, fixture)
	if len(added) != 0 || len(paths) != 0 {
		t.Fatalf("lifecycle correctness: expansion after revision replacement must be empty, got added=%#v paths=%#v", added, paths)
	}
	expandedSet := chunkIDSet(added)
	if containsAny(expandedSet, fixture.RelevantChunkIDs) || containsAny(expandedSet, fixture.NoiseChunkIDs) || containsAny(expandedSet, fixture.StaleChunkIDs) {
		t.Fatalf("lifecycle correctness: old-revision evidence leaked after invalidation: %#v", added)
	}
}
