package format

import (
	"strings"

	"qingqiu-world-server/internal/model"
)

type plainTextAdapter struct{}

func (plainTextAdapter) ID() string { return "plain-text-v1" }

func (plainTextAdapter) Parse(document Document) ([]Block, error) {
	if document.FileType == "pdf" && len(document.Pages) > 0 {
		pages := make([]Block, 0, len(document.Pages))
		for _, page := range document.Pages {
			rangeValue := Range{Start: page.Start, End: page.End}
			pages = append(pages, Block{NodeType: model.ContentNodeTypePage, Text: document.Text[page.Start:page.End], SelfRange: rangeValue, SubtreeRange: rangeValue, Children: paragraphBlocks(document.Text, rangeValue)})
		}
		return pages, nil
	}
	return paragraphBlocks(document.Text, Range{End: len(document.Text)}), nil
}

func paragraphBlocks(source string, scope Range) []Block {
	blocks, start := make([]Block, 0), -1
	for lineStart := scope.Start; lineStart < scope.End; {
		lineEnd := strings.IndexByte(source[lineStart:scope.End], '\n')
		if lineEnd < 0 {
			lineEnd = scope.End
		} else {
			lineEnd += lineStart + 1
		}
		if strings.TrimSpace(source[lineStart:lineEnd]) == "" {
			if start >= 0 {
				blocks = append(blocks, leaf(model.ContentNodeTypeParagraph, source, Range{Start: start, End: lineStart}))
				start = -1
			}
		} else if start < 0 {
			start = lineStart
		}
		lineStart = lineEnd
	}
	if start >= 0 {
		blocks = append(blocks, leaf(model.ContentNodeTypeParagraph, source, Range{Start: start, End: scope.End}))
	}
	return blocks
}

func leaf(nodeType model.ContentNodeType, source string, rangeValue Range) Block {
	return Block{NodeType: nodeType, Text: source[rangeValue.Start:rangeValue.End], SelfRange: rangeValue, SubtreeRange: rangeValue}
}
