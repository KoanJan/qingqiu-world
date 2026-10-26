package tools

import (
	"testing"

	"qingqiu-world-server/internal/service/kb"
)

func TestBuildScanKBResponse_KeepsMetadataWhenEvidenceIsTruncated(t *testing.T) {
	result := &kb.ScanResult{
		Status:  kb.ScanStatusPartial,
		Outcome: "retrieval completed with active evidence",
		RelatedEvidence: []kb.Evidence{
			{KnowledgeBaseID: 2, DocumentID: 4, RevisionID: 7, DocumentTitle: "first", RetrievalUnitID: 10, LocatorJSON: "{\"page\":1}", ExpansionKind: 0, Content: "one two three"},
			{KnowledgeBaseID: 2, DocumentID: 5, RevisionID: 8, DocumentTitle: "second", RetrievalUnitID: 11, LocatorJSON: "{\"page\":2}", ExpansionKind: 1, Content: "four five six"},
		},
	}

	response := buildScanKBResponse(result, 4)
	if !response.EvidenceTruncated || response.TruncationNotice == "" {
		t.Fatalf("expected explicit evidence truncation notice: %#v", response)
	}
	if len(response.RelatedDocuments) != 2 || len(response.RelatedEvidence) != 2 {
		t.Fatalf("metadata must not be truncated: %#v", response)
	}
	if response.RelatedEvidence[0].ChunkID != 10 || response.RelatedEvidence[1].ChunkID != 11 {
		t.Fatalf("related evidence IDs changed: %#v", response.RelatedEvidence)
	}
	if len(response.Evidence) != 2 || response.Evidence[1].ChunkID != 11 || response.Evidence[1].Content == "four five six" {
		t.Fatalf("only evidence content may be truncated: %#v", response.Evidence)
	}
	if len(response.SupportedConclusions) != 0 || len(response.Logic) != 0 {
		t.Fatalf("scan_kb must not expose task-level conclusions: %#v", response)
	}
}

func TestReadChunkIDs_DeduplicatesPositiveJSONNumbers(t *testing.T) {
	ids := readChunkIDs([]interface{}{float64(11), float64(11), float64(0), float64(-1), "bad", float64(12)})
	if len(ids) != 2 || ids[0] != 11 || ids[1] != 12 {
		t.Fatalf("unexpected parsed IDs: %#v", ids)
	}
}
