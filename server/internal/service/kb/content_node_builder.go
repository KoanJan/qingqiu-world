package kb

import (
	"fmt"
	"strings"

	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/kb/format"
)

// sourceRange is a half-open byte range in the canonical source rendition.
type sourceRange struct{ Start, End int }

// parsedBlock is the Builder's private projection of a format.Block.
type parsedBlock struct {
	NodeType                model.ContentNodeType
	Text                    string
	SelfRange, SubtreeRange sourceRange
	HeadingLevel            int
	Children                []parsedBlock
}

// contentTreeNode is the in-memory canonical tree. It is intentionally kept
// separate from persistence so construction invariants are testable without a
// database and no parser-private intermediate nodes can leak into storage.
type contentTreeNode struct {
	NodeType        model.ContentNodeType
	Text            string
	SelfRange       sourceRange
	SubtreeRange    sourceRange
	HeadingLevel    int
	Parent          *contentTreeNode
	Children        []*contentTreeNode
	leafPersistedID int64
}

func parsedBlocksFromFormat(blocks []format.Block) []parsedBlock {
	result := make([]parsedBlock, len(blocks))
	for index, block := range blocks {
		result[index] = parsedBlock{NodeType: block.NodeType, Text: block.Text, SelfRange: sourceRange{Start: block.SelfRange.Start, End: block.SelfRange.End}, SubtreeRange: sourceRange{Start: block.SubtreeRange.Start, End: block.SubtreeRange.End}, HeadingLevel: block.HeadingLevel, Children: parsedBlocksFromFormat(block.Children)}
	}
	return result
}

// buildContentTree projects parser facts into the canonical structural tree.
// It only uses deterministic structural rules; token counts control granularity
// and never create semantic relationships.
func buildContentTree(title, source string, blocks []parsedBlock, profile retrievalTokenProfile, tokenCount func(string) int) (*contentTreeNode, error) {
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("cannot build content tree from empty source")
	}
	if tokenCount == nil {
		return nil, fmt.Errorf("content tree requires a token counter")
	}
	root := &contentTreeNode{
		NodeType:     model.ContentNodeTypeDocument,
		Text:         title,
		SelfRange:    sourceRange{},
		SubtreeRange: sourceRange{Start: 0, End: len(source)},
	}
	// A small document is one retrievable body. This prevents mechanical
	// structural detail (for example a short profile's fields) from becoming
	// many tiny leaves without losing its document-level provenance.
	body := &contentTreeNode{
		NodeType:     model.ContentNodeTypeDocumentBody,
		Text:         source,
		SelfRange:    sourceRange{Start: 0, End: len(source)},
		SubtreeRange: sourceRange{Start: 0, End: len(source)},
	}
	appendTreeChild(root, body)
	if treeNodeSearchTokenCount(title, body, tokenCount) <= profile.MaxTokens {
		return root, nil
	}
	root.Children = nil

	headingStack := make([]*contentTreeNode, 0)
	for _, block := range blocks {
		node, err := treeNodeFromParsedBlock(source, block)
		if err != nil {
			return nil, err
		}
		if node.NodeType == model.ContentNodeTypeHeading {
			for len(headingStack) > 0 && headingStack[len(headingStack)-1].HeadingLevel >= node.HeadingLevel {
				headingStack = headingStack[:len(headingStack)-1]
			}
			parent := root
			if len(headingStack) > 0 {
				parent = headingStack[len(headingStack)-1]
			}
			appendTreeChild(parent, node)
			headingStack = append(headingStack, node)
			continue
		}
		parent := root
		if len(headingStack) > 0 {
			parent = headingStack[len(headingStack)-1]
		}
		appendTreeChild(parent, node)
	}
	updateSubtreeRanges(root)
	aggregateShortLeaves(root, title, source, profile, tokenCount)
	if err := validateAggregateBudgets(root, title, profile, tokenCount); err != nil {
		return nil, err
	}
	if err := validateContentTree(root, len(source)); err != nil {
		return nil, err
	}
	return root, nil
}

func validateAggregateBudgets(node *contentTreeNode, title string, profile retrievalTokenProfile, tokenCount func(string) int) error {
	if node.NodeType == model.ContentNodeTypeAggregate && treeNodeSearchTokenCount(title, node, tokenCount) > profile.MaxTokens {
		return fmt.Errorf("aggregate exceeds retrieval token budget: %d > %d", treeNodeSearchTokenCount(title, node, tokenCount), profile.MaxTokens)
	}
	for _, child := range node.Children {
		if err := validateAggregateBudgets(child, title, profile, tokenCount); err != nil {
			return err
		}
	}
	return nil
}

func treeNodeFromParsedBlock(source string, block parsedBlock) (*contentTreeNode, error) {
	if block.SelfRange.Start < 0 || block.SelfRange.End > len(source) || block.SelfRange.Start >= block.SelfRange.End {
		return nil, fmt.Errorf("invalid parsed block range [%d,%d)", block.SelfRange.Start, block.SelfRange.End)
	}
	if source[block.SelfRange.Start:block.SelfRange.End] != block.Text {
		return nil, fmt.Errorf("parsed block text differs from source range [%d,%d)", block.SelfRange.Start, block.SelfRange.End)
	}
	node := &contentTreeNode{NodeType: block.NodeType, Text: block.Text, SelfRange: block.SelfRange, SubtreeRange: block.SubtreeRange, HeadingLevel: block.HeadingLevel}
	for _, child := range block.Children {
		childNode, err := treeNodeFromParsedBlock(source, child)
		if err != nil {
			return nil, err
		}
		appendTreeChild(node, childNode)
	}
	return node, nil
}

func appendTreeChild(parent, child *contentTreeNode) {
	child.Parent = parent
	parent.Children = append(parent.Children, child)
}

func updateSubtreeRanges(node *contentTreeNode) {
	for _, child := range node.Children {
		updateSubtreeRanges(child)
	}
	if len(node.Children) == 0 || node.NodeType == model.ContentNodeTypeDocument {
		return
	}
	last := node.Children[len(node.Children)-1]
	if last.SubtreeRange.End > node.SubtreeRange.End {
		node.SubtreeRange.End = last.SubtreeRange.End
	}
}

// aggregateShortLeaves replaces only adjacent short leaf candidates under one
// parent. It never combines across a structural boundary or retains hidden
// member nodes under Aggregate.
func aggregateShortLeaves(node *contentTreeNode, title, source string, profile retrievalTokenProfile, tokenCount func(string) int) {
	for _, child := range node.Children {
		aggregateShortLeaves(child, title, source, profile, tokenCount)
	}
	if len(node.Children) < 2 {
		return
	}
	rebuilt := make([]*contentTreeNode, 0, len(node.Children))
	for index := 0; index < len(node.Children); {
		child := node.Children[index]
		if len(child.Children) != 0 || treeNodeSearchTokenCount(title, child, tokenCount) >= profile.MinTokens {
			rebuilt = append(rebuilt, child)
			index++
			continue
		}
		end := index
		for end < len(node.Children) {
			candidate := node.Children[end]
			if len(candidate.Children) != 0 || treeNodeSearchTokenCount(title, candidate, tokenCount) >= profile.MinTokens {
				break
			}
			end++
		}
		rebuilt = append(rebuilt, greedyAggregateRun(node, title, source, node.Children[index:end], profile, tokenCount)...)
		index = end
	}
	node.Children = rebuilt
	for _, child := range rebuilt {
		child.Parent = node
	}
}

// greedyAggregateRun uses forward packing followed by tail repair. The first
// objective is avoiding an avoidable under-minimum aggregate; only then does
// it minimize excess over the retrieval minimum.
func greedyAggregateRun(parent *contentTreeNode, title, source string, run []*contentTreeNode, profile retrievalTokenProfile, tokenCount func(string) int) []*contentTreeNode {
	groups := make([][]*contentTreeNode, 0)
	for index := 0; index < len(run); {
		group := make([]*contentTreeNode, 0)
		for index < len(run) {
			trial := append(append([]*contentTreeNode{}, group...), run[index])
			if len(group) > 0 && aggregateTokenCount(parent, title, source, trial, tokenCount) > profile.MaxTokens {
				break
			}
			group = trial
			index++
			if aggregateTokenCount(parent, title, source, group, tokenCount) >= profile.MinTokens {
				break
			}
		}
		if len(group) == 0 {
			applogger.Warn("short leaf cannot fit aggregate retrieval budget", "parent_type", parent.NodeType, "max_tokens", profile.MaxTokens)
			group = append(group, run[index])
			index++
		}
		groups = append(groups, group)
	}
	if len(groups) >= 2 && aggregateTokenCount(parent, title, source, groups[len(groups)-1], tokenCount) < profile.MinTokens {
		groups = repairAggregateTail(parent, title, source, groups, profile, tokenCount)
	}
	result := make([]*contentTreeNode, 0, len(groups))
	for _, group := range groups {
		result = append(result, newAggregateNode(parent, source, group))
	}
	return result
}

func repairAggregateTail(parent *contentTreeNode, title, source string, groups [][]*contentTreeNode, profile retrievalTokenProfile, tokenCount func(string) int) [][]*contentTreeNode {
	last := len(groups) - 1
	previous, tail := groups[last-1], groups[last]
	combined := append(append([]*contentTreeNode{}, previous...), tail...)
	if aggregateTokenCount(parent, title, source, combined, tokenCount) <= profile.MaxTokens {
		groups[last-1] = combined
		return groups[:last]
	}
	for moved := 1; moved < len(previous); moved++ {
		left := previous[:len(previous)-moved]
		right := append(append([]*contentTreeNode{}, previous[len(previous)-moved:]...), tail...)
		if aggregateTokenCount(parent, title, source, left, tokenCount) >= profile.MinTokens && aggregateTokenCount(parent, title, source, right, tokenCount) >= profile.MinTokens && aggregateTokenCount(parent, title, source, right, tokenCount) <= profile.MaxTokens {
			groups[last-1], groups[last] = left, right
			return groups
		}
	}
	applogger.Warn("aggregate tail remains below retrieval minimum", "parent_type", parent.NodeType, "tail_tokens", aggregateTokenCount(parent, title, source, tail, tokenCount), "min_tokens", profile.MinTokens)
	return groups
}

func newAggregateNode(parent *contentTreeNode, source string, members []*contentTreeNode) *contentTreeNode {
	start := members[0].SelfRange.Start
	end := members[len(members)-1].SelfRange.End
	return &contentTreeNode{NodeType: model.ContentNodeTypeAggregate, Text: source[start:end], SelfRange: sourceRange{Start: start, End: end}, SubtreeRange: sourceRange{Start: start, End: end}, Parent: parent}
}

func aggregateTokenCount(parent *contentTreeNode, title, source string, members []*contentTreeNode, tokenCount func(string) int) int {
	if len(members) == 0 {
		return 0
	}
	start, end := members[0].SelfRange.Start, members[len(members)-1].SelfRange.End
	probe := &contentTreeNode{NodeType: model.ContentNodeTypeAggregate, Text: source[start:end], Parent: parent}
	return treeNodeSearchTokenCount(title, probe, tokenCount)
}

func treeNodeSearchTokenCount(title string, node *contentTreeNode, tokenCount func(string) int) int {
	return tokenCount(treeNodeSearchText(title, node))
}

// treeNodeSearchText is the one deterministic context encoding used both for
// budgeting and later embedding. Display text remains exact source text.
func treeNodeSearchText(title string, node *contentTreeNode) string {
	path := headingPath(node)
	context := title
	if len(path) > 0 {
		context += " > " + strings.Join(path, " > ")
	}
	return fmt.Sprintf("Document: %s\nStructure: %s\nLeafType: %d\nSourceRange: %d-%d\n\n%s", title, context, node.NodeType, node.SelfRange.Start, node.SelfRange.End, node.Text)
}

// headingPath returns the structural headings that contain node in source
// order. It is shared by search-text and persisted context serialization.
func headingPath(node *contentTreeNode) []string {
	path := make([]string, 0)
	for cursor := node.Parent; cursor != nil && cursor.NodeType != model.ContentNodeTypeDocument; cursor = cursor.Parent {
		if cursor.NodeType == model.ContentNodeTypeHeading {
			path = append([]string{strings.TrimSpace(cursor.Text)}, path...)
		}
	}
	return path
}

func validateContentTree(node *contentTreeNode, sourceLen int) error {
	if node.NodeType != model.ContentNodeTypeDocument {
		if node.SelfRange.Start < 0 || node.SelfRange.End > sourceLen || node.SelfRange.Start >= node.SelfRange.End {
			return fmt.Errorf("node type %d has invalid self range [%d,%d)", node.NodeType, node.SelfRange.Start, node.SelfRange.End)
		}
	}
	for _, child := range node.Children {
		if child.SubtreeRange.Start < node.SubtreeRange.Start || child.SubtreeRange.End > node.SubtreeRange.End {
			return fmt.Errorf("child subtree [%d,%d) lies outside parent subtree [%d,%d)", child.SubtreeRange.Start, child.SubtreeRange.End, node.SubtreeRange.Start, node.SubtreeRange.End)
		}
		if err := validateContentTree(child, sourceLen); err != nil {
			return err
		}
	}
	return nil
}
