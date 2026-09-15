package kb

import "testing"

func TestTruncateEvidenceContent_PreservesEveryEvidenceItem(t *testing.T) {
	evidence := []Evidence{
		{RetrievalUnitID: 41, Content: "one two three"},
		{RetrievalUnitID: 42, Content: "four five six"},
	}
	trimmed, truncated, usedTokens := TruncateEvidenceContent(evidence, 4)
	if !truncated {
		t.Fatal("expected body truncation")
	}
	if usedTokens > 4 {
		t.Fatalf("token budget exceeded: %d", usedTokens)
	}
	if len(trimmed) != len(evidence) {
		t.Fatalf("evidence metadata was dropped: got %d items", len(trimmed))
	}
	if trimmed[0].RetrievalUnitID != 41 || trimmed[1].RetrievalUnitID != 42 {
		t.Fatalf("evidence provenance changed: %#v", trimmed)
	}
	if trimmed[1].Content == "four five six" {
		t.Fatalf("later evidence body was not truncated: %q", trimmed[1].Content)
	}
}

func TestEstimateTextTokens_UsesCharacterLength(t *testing.T) {
	if got := EstimateTextTokens(""); got != 0 {
		t.Fatalf("empty text estimate: got %d", got)
	}
	if got := EstimateTextTokens("abcd"); got != 2 {
		t.Fatalf("ASCII character estimate: got %d", got)
	}
	if got := EstimateTextTokens("你好啊"); got != 2 {
		t.Fatalf("Unicode character estimate: got %d", got)
	}
}

func TestAppendStructuralNeighbours_RetainsAnchorsBeforeContext(t *testing.T) {
	anchors := []Evidence{{RetrievalUnitID: 90, ExpansionKind: EvidenceExpansionKindAnchor}, {RetrievalUnitID: 91, ExpansionKind: EvidenceExpansionKindAnchor}}
	neighbours := [][]Evidence{{{RetrievalUnitID: 1, ExpansionKind: EvidenceExpansionKindStructuralContext}, {RetrievalUnitID: 2, ExpansionKind: EvidenceExpansionKindStructuralContext}}}
	got := appendStructuralNeighbours(anchors, neighbours, 3)
	if len(got) != 3 {
		t.Fatalf("unexpected evidence count: %d", len(got))
	}
	if got[0].RetrievalUnitID != 90 || got[1].RetrievalUnitID != 91 || got[2].RetrievalUnitID != 1 {
		t.Fatalf("context displaced a ranked anchor: %#v", got)
	}
}
