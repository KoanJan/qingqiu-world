package kb

import "testing"

// TestBM25IndexSearch_UsesChunkIDAsScoreTieBreak ensures map iteration cannot
// change the retrieval order of otherwise identical BM25 candidates.
func TestBM25IndexSearch_UsesChunkIDAsScoreTieBreak(t *testing.T) {
	idx := newBM25Index()
	idx.AddDocuments([]bm25ChunkDoc{
		{ChunkID: 9, Content: "same token"},
		{ChunkID: 3, Content: "same token"},
	})
	got := idx.Search("same", 2)
	if len(got) != 2 {
		t.Fatalf("unexpected result count: %d", len(got))
	}
	if got[0].ChunkID != 3 || got[1].ChunkID != 9 {
		t.Fatalf("equal-score order must be stable by chunk ID: %#v", got)
	}
}
