package kb

import "testing"

func TestGroundedRelationInputFromCandidateUsesQuotedEvidenceOnly(t *testing.T) {
	evidence := map[int64]analyzerEvidence{
		10: {
			Handle:        traceEvidenceHandle{ChunkID: 10, KnowledgeBaseID: 2, DocumentID: 3, RevisionID: 4, LocatorJSON: "{}"},
			Content:       "Service A stores data in PostgreSQL.",
			ContentNodeID: 30,
			ContentHash:   "hash-10",
		},
		11: {
			Handle:        traceEvidenceHandle{ChunkID: 11, KnowledgeBaseID: 2, DocumentID: 3, RevisionID: 4, LocatorJSON: "{}"},
			Content:       "Another nearby paragraph.",
			ContentNodeID: 31,
			ContentHash:   "hash-11",
		},
	}

	input, ok := groundedRelationInputFromCandidate(relationCandidate{
		Subject:        "Service A",
		Predicate:      "uses",
		Object:         "PostgreSQL",
		EvidenceChunks: []int64{10, 11},
		SupportQuote:   "stores data in PostgreSQL",
	}, evidence)
	if !ok {
		t.Fatal("candidate with one quoted cited chunk should be accepted")
	}
	if len(input.Evidence) != 1 || input.Evidence[0].ChunkID != 10 {
		t.Fatalf("expected only quoted evidence chunk to ground relation: %#v", input.Evidence)
	}
}

func TestGroundedRelationInputFromCandidateRejectsUnknownChunk(t *testing.T) {
	_, ok := groundedRelationInputFromCandidate(relationCandidate{
		Subject:        "Service A",
		Predicate:      "uses",
		Object:         "PostgreSQL",
		EvidenceChunks: []int64{99},
		SupportQuote:   "stores data in PostgreSQL",
	}, map[int64]analyzerEvidence{})
	if ok {
		t.Fatal("candidate citing unknown evidence must be rejected")
	}
}
