package kb

import (
	"testing"

	"qingqiu-world-server/internal/model"
)

func TestAlignPersistedChunkOffsets_TreatsLegacyWhitespaceAsEquivalent(t *testing.T) {
	rendered := extractedDocument{Text: "heading\n\nfirst line\nsecond line\nclosing"}
	chunks := []model.DocumentChunk{{
		ID:          1,
		ChunkIndex:  0,
		DisplayText: "first line second line",
		StartOffset: 9,
		EndOffset:   31,
	}}
	aligned, err := alignPersistedChunkOffsets(model.Document{ID: 42}, rendered, chunks)
	if err != nil {
		t.Fatalf("align offsets: %v", err)
	}
	if !aligned[1] {
		t.Fatal("expected legacy whitespace-normalized chunk to be aligned")
	}
}

func TestNormalizedLocatorTextFind_MapsToOriginalWhitespaceRange(t *testing.T) {
	source := "before\n\nalpha\t beta\nafter"
	start, end, found := normalizeLocatorSearchText(source).find("alpha beta", 0)
	if !found {
		t.Fatal("expected normalized content to be found")
	}
	if got := source[start:end]; got != "alpha\t beta" {
		t.Fatalf("unexpected original range %q", got)
	}
}
