package kb

import (
	"encoding/json"
	"testing"

	"qingqiu-world-server/internal/model"
)

func TestSerializeStructureContextIncludesDirectRetrievalLocation(t *testing.T) {
	text := "# Overview\nAda\n"
	root := &contentTreeNode{NodeType: model.ContentNodeTypeDocument}
	heading := &contentTreeNode{NodeType: model.ContentNodeTypeHeading, Text: "# Overview\n", Parent: root}
	leaf := &contentTreeNode{NodeType: model.ContentNodeTypeListItem, Text: "Ada\n", SelfRange: sourceRange{Start: 11, End: len(text)}, SubtreeRange: sourceRange{Start: 11, End: len(text)}, Parent: heading, leafPersistedID: 42}
	unit := generatedChunk{leaf: leaf, content: leaf.Text, start: 11, end: len(text)}
	rendered := extractedDocument{Text: text, FileType: "pdf", pages: []extractedPage{{Number: 1, Start: 0, End: len(text)}}}
	encoded, err := serializeStructureContext("Profile", 9, unit, rendered)
	if err != nil {
		t.Fatalf("serializeStructureContext() error = %v", err)
	}
	var context structureContext
	if err := json.Unmarshal([]byte(encoded), &context); err != nil {
		t.Fatalf("unmarshal context: %v", err)
	}
	if context.DocumentTitle != "Profile" || context.FileType != "pdf" || context.SourceKind != 0 || context.LeafNodeID != 42 || context.LeafNodeType != model.ContentNodeTypeListItem || context.RevisionID != 9 {
		t.Fatalf("unexpected context identity: %#v", context)
	}
	if len(context.HeadingPath) != 1 || context.HeadingPath[0] != "# Overview" {
		t.Fatalf("heading path = %#v", context.HeadingPath)
	}
	if context.ChunkStart != 11 || context.ChunkEnd != len(text) || context.LineStart != 2 || context.LineEnd != 3 || context.LeafStart != 11 || context.LeafEnd != len(text) || context.PageStart != 1 || context.PageEnd != 1 {
		t.Fatalf("unexpected context range: %#v", context)
	}
}

func TestChunkLocatorFromStructureContextUsesChunkCoordinates(t *testing.T) {
	context := structureContext{SourceKind: 0, FileType: "txt", RevisionID: 7, ChunkStart: 10, ChunkEnd: 20, LineStart: 2, LineEnd: 3}
	encoded, err := json.Marshal(context)
	if err != nil {
		t.Fatalf("marshal context: %v", err)
	}
	chunk := model.DocumentChunk{ID: 99, RevisionID: 7, ChunkIndex: 4, StartOffset: 10, EndOffset: 20, StructureContextJSON: string(encoded)}
	locatorJSON, ok := chunkLocatorFromStructureContext(chunk)
	if !ok {
		t.Fatal("chunk context should produce a direct locator")
	}
	var locator evidenceLocator
	if err := json.Unmarshal([]byte(locatorJSON), &locator); err != nil {
		t.Fatalf("decode locator: %v", err)
	}
	if locator.ChunkIndex != 4 || locator.CharStart != 10 || locator.CharEnd != 20 || locator.SelfStart != 10 || locator.SubtreeEnd != 20 {
		t.Fatalf("unexpected chunk locator: %#v", locator)
	}
}

func TestChunkLocatorFromMatchingNodeSupportsOneToOneLegacyContext(t *testing.T) {
	node := model.ContentNode{ID: 42, LocatorJSON: `{"source_kind":0,"file_type":"txt","chunk_index":-1,"char_start":10,"char_end":20,"line_start":2,"line_end":3,"page_start":0,"page_end":0,"self_start":10,"self_end":20,"subtree_start":10,"subtree_end":20}`}
	chunk := model.DocumentChunk{ID: 99, ChunkIndex: 4, StartOffset: 10, EndOffset: 20}
	locatorJSON, ok := chunkLocatorFromMatchingNode(chunk, node)
	if !ok {
		t.Fatal("matching node and chunk must produce a direct compatibility locator")
	}
	var locator evidenceLocator
	if err := json.Unmarshal([]byte(locatorJSON), &locator); err != nil {
		t.Fatalf("decode locator: %v", err)
	}
	if locator.ChunkIndex != 4 || locator.CharStart != 10 || locator.CharEnd != 20 {
		t.Fatalf("unexpected compatibility locator: %#v", locator)
	}
}
