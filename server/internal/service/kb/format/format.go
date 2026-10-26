// Package format converts canonical document renditions into structural facts.
// It never infers semantic relationships or depends on the KB processing flow.
package format

import (
	"fmt"

	"qingqiu-world-server/internal/model"
)

// Range is a half-open byte range in Document.Text.
type Range struct{ Start, End int }

// Page records an already extracted source-page range.
type Page struct{ Number, Start, End int }

// Document is the normalized input made available to a format adapter.
type Document struct {
	Text, FileType string
	Pages          []Page
}

// Block is one syntax-grounded structural fact consumed by ContentNode Builder.
type Block struct {
	NodeType                model.ContentNodeType
	Text                    string
	SelfRange, SubtreeRange Range
	HeadingLevel            int
	Children                []Block
}

// Adapter reports only verifiable structural facts for one document format.
type Adapter interface {
	Parse(Document) ([]Block, error)
	ID() string
}

// Select returns the deterministic adapter for a supported canonical format.
func Select(fileType string) (Adapter, error) {
	switch fileType {
	case "md":
		return markdownAdapter{}, nil
	case "txt", "pdf":
		return plainTextAdapter{}, nil
	default:
		return nil, fmt.Errorf("no format adapter for file type %q", fileType)
	}
}
