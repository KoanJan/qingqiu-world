package kb

import (
	"encoding/json"
	"testing"
)

func TestNormalizeExtractedText_JoinsHyphenatedWordWrap(t *testing.T) {
	text := normalizeExtractedText("A process-\ning pipeline.\r\n\r\nNext paragraph.")
	want := "A processing pipeline.\n\nNext paragraph."
	if text != want {
		t.Fatalf("unexpected normalized text: got %q, want %q", text, want)
	}
}

func TestNormalizeExtractedText_PreservesDashBeforeUppercase(t *testing.T) {
	text := normalizeExtractedText("Section-\nTitle")
	if text != "Section-\nTitle" {
		t.Fatalf("heading dash must not be joined: %q", text)
	}
}

func TestHasSuspiciousPDFWordRuns(t *testing.T) {
	longWord := ""
	for i := 0; i < 80; i++ {
		longWord += "a"
	}
	if !hasSuspiciousPDFWordRuns(longWord) {
		t.Fatal("expected a long collapsed word run to be reported")
	}
	if hasSuspiciousPDFWordRuns("well spaced PDF text") {
		t.Fatal("ordinary text must not be reported")
	}
	collapsed := ""
	for i := 0; i < 220; i++ {
		collapsed += "a"
	}
	if !hasSuspiciousPDFWordRuns(collapsed) {
		t.Fatal("expected ASCII-heavy text without spacing to be reported")
	}
}

func TestExtractedDocumentLocatorJSON_ContainsCompleteLocalProvenance(t *testing.T) {
	document := extractedDocument{Text: "first line\nsecond line", FileType: "txt"}
	var locator evidenceLocator
	if err := json.Unmarshal([]byte(document.locatorJSON(7, 11, 22)), &locator); err != nil {
		t.Fatalf("decode locator: %v", err)
	}
	if locator.SourceKind != 0 || locator.FileType != "txt" || locator.ChunkIndex != 7 {
		t.Fatalf("unexpected source metadata: %#v", locator)
	}
	if locator.CharStart != 11 || locator.CharEnd != 22 || locator.LineStart != 2 || locator.LineEnd != 2 {
		t.Fatalf("unexpected text location: %#v", locator)
	}
	if locator.PageStart != 0 || locator.PageEnd != 0 {
		t.Fatalf("plain text must not invent page metadata: %#v", locator)
	}
}

func TestExtractedDocumentLocatorJSON_PreservesPDFPageRange(t *testing.T) {
	document := extractedDocument{
		Text:     "page one\n\npage two",
		FileType: "pdf",
		pages: []extractedPage{
			{Number: 1, Start: 0, End: 8},
			{Number: 2, Start: 10, End: 18},
		},
	}
	var locator evidenceLocator
	if err := json.Unmarshal([]byte(document.locatorJSON(2, 0, len(document.Text))), &locator); err != nil {
		t.Fatalf("decode locator: %v", err)
	}
	if locator.PageStart != 1 || locator.PageEnd != 2 {
		t.Fatalf("expected two-page provenance, got %#v", locator)
	}
}

func TestLocalNodeMetadataJSON_PersistsCurrentRenderingVersion(t *testing.T) {
	var metadata struct {
		SourceKind       int    `json:"source_kind"`
		FileType         string `json:"file_type"`
		Role             string `json:"role"`
		RenderingVersion int    `json:"source_rendering_version"`
	}
	if err := json.Unmarshal([]byte(localNodeMetadataJSON("pdf", "chunk")), &metadata); err != nil {
		t.Fatalf("decode node metadata: %v", err)
	}
	if metadata.SourceKind != 0 || metadata.FileType != "pdf" || metadata.Role != "chunk" || metadata.RenderingVersion != sourceRenderingVersionCurrent {
		t.Fatalf("unexpected node metadata: %#v", metadata)
	}
}

func TestSourceRenderingVersionFromMetadata_DistinguishesLegacyAndCurrent(t *testing.T) {
	if version, known := sourceRenderingVersionFromMetadata(`{"source_rendering_version":1}`); !known || version != sourceRenderingVersionLegacy {
		t.Fatalf("legacy version not recognized: version=%d known=%t", version, known)
	}
	if version, known := sourceRenderingVersionFromMetadata(`{"source_rendering_version":2}`); !known || version != sourceRenderingVersionCurrent {
		t.Fatalf("current version not recognized: version=%d known=%t", version, known)
	}
	if _, known := sourceRenderingVersionFromMetadata(`{"source_kind":0}`); known {
		t.Fatal("versionless legacy metadata must require migration")
	}
}
