package format

import (
	"fmt"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"qingqiu-world-server/internal/model"
)

type markdownAdapter struct{}

func (markdownAdapter) ID() string { return "goldmark-v1" }
func (markdownAdapter) Parse(document Document) ([]Block, error) {
	source := []byte(document.Text)
	root := goldmark.New(goldmark.WithExtensions(extension.Table)).Parser().Parse(text.NewReader(source))
	blocks := make([]Block, 0, root.ChildCount())
	for child := root.FirstChild(); child != nil; child = child.NextSibling() {
		block, err := markdownBlock(source, child)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}
func markdownBlock(source []byte, node ast.Node) (Block, error) {
	rangeValue, ok := nodeRange(source, node)
	if !ok {
		return Block{}, fmt.Errorf("Markdown AST node %s has no source range", node.Kind())
	}
	block := Block{NodeType: model.ContentNodeTypeParagraph, Text: string(source[rangeValue.Start:rangeValue.End]), SelfRange: rangeValue, SubtreeRange: rangeValue}
	switch value := node.(type) {
	case *ast.Heading:
		block.NodeType = model.ContentNodeTypeHeading
		block.HeadingLevel = value.Level
	case *ast.List:
		block.NodeType = model.ContentNodeTypeList
		block.Children = markdownChildren(source, value, model.ContentNodeTypeListItem)
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		block.NodeType = model.ContentNodeTypeCode
	case *extast.Table:
		block.NodeType = model.ContentNodeTypeTable
		block.Children = tableRows(source, value)
	}
	return block, nil
}
func markdownChildren(source []byte, parent ast.Node, nodeType model.ContentNodeType) []Block {
	result := make([]Block, 0, parent.ChildCount())
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		if r, ok := nodeRange(source, child); ok {
			result = append(result, leaf(nodeType, string(source), r))
		}
	}
	return result
}
func tableRows(source []byte, table *extast.Table) []Block {
	result := make([]Block, 0, table.ChildCount())
	for child := table.FirstChild(); child != nil; child = child.NextSibling() {
		switch child.(type) {
		case *extast.TableHeader, *extast.TableRow:
			if r, ok := nodeRange(source, child); ok {
				result = append(result, leaf(model.ContentNodeTypeTableRow, string(source), r))
			}
		}
	}
	return result
}
func nodeRange(source []byte, node ast.Node) (Range, bool) {
	start, end, ok := nodeBounds(node)
	if !ok {
		return Range{}, false
	}
	for start > 0 && source[start-1] != '\n' {
		start--
	}
	for end < len(source) && source[end] != '\n' {
		end++
	}
	if end < len(source) {
		end++
	}
	return Range{start, end}, start < end
}
func nodeBounds(node ast.Node) (int, int, bool) {
	start, end, found := 0, 0, false
	if node.Type() == ast.TypeBlock {
		lines := node.Lines()
		for i := 0; i < lines.Len(); i++ {
			s := lines.At(i)
			if !found || s.Start < start {
				start = s.Start
			}
			if !found || s.Stop > end {
				end = s.Stop
			}
			found = true
		}
	}
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		s, e, ok := nodeBounds(child)
		if ok {
			if !found || s < start {
				start = s
			}
			if !found || e > end {
				end = e
			}
			found = true
		}
	}
	return start, end, found
}
