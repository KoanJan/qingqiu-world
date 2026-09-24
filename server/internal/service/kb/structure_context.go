package kb

import (
	"encoding/json"

	"qingqiu-world-server/internal/model"
)

// structureContext is self-sufficient retrieval provenance. It lets a caller
// show a chunk's document location without loading the content-node tree.
type structureContext struct {
	DocumentTitle string                `json:"document_title"`
	SourceKind    int                   `json:"source_kind"`
	FileType      string                `json:"file_type"`
	HeadingPath   []string              `json:"heading_path"`
	LeafNodeID    int64                 `json:"leaf_node_id"`
	LeafNodeType  model.ContentNodeType `json:"leaf_node_type"`
	RevisionID    int64                 `json:"revision_id"`
	ChunkStart    int                   `json:"chunk_start"`
	ChunkEnd      int                   `json:"chunk_end"`
	LineStart     int                   `json:"line_start"`
	LineEnd       int                   `json:"line_end"`
	LeafStart     int                   `json:"leaf_start"`
	LeafEnd       int                   `json:"leaf_end"`
	PageStart     int                   `json:"page_start"`
	PageEnd       int                   `json:"page_end"`
}

func serializeStructureContext(title string, revisionID int64, unit generatedChunk, rendered extractedDocument) (string, error) {
	pageStart, pageEnd := rendered.pageRange(unit.start, unit.end)
	context := structureContext{
		DocumentTitle: title,
		SourceKind:    0,
		FileType:      rendered.FileType,
		HeadingPath:   headingPath(unit.leaf),
		LeafNodeID:    unit.leaf.leafPersistedID,
		LeafNodeType:  unit.leaf.NodeType,
		RevisionID:    revisionID,
		ChunkStart:    unit.start,
		ChunkEnd:      unit.end,
		LineStart:     lineAtOffset(rendered.Text, unit.start),
		LineEnd:       lineAtOffset(rendered.Text, unit.end),
		LeafStart:     unit.leaf.SelfRange.Start,
		LeafEnd:       unit.leaf.SelfRange.End,
		PageStart:     pageStart,
		PageEnd:       pageEnd,
	}
	encoded, err := json.Marshal(context)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
