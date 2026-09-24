package kb

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// generatedChunk is one retrieval unit projected from exactly one final leaf.
type generatedChunk struct {
	leaf       *contentTreeNode
	content    string
	start, end int
	searchText string
}

// generateChunks creates one unit per normal leaf and linearly splits only an
// oversized leaf. It never packs neighboring leaves: that is Builder work.
func generateChunks(root *contentTreeNode, title string, splitter *textSplitter, profile retrievalTokenProfile) ([]generatedChunk, error) {
	if splitter == nil || splitter.Err() != nil || splitter.tp == nil {
		return nil, fmt.Errorf("chunk generator requires initialized tokenizer")
	}
	var result []generatedChunk
	var visit func(*contentTreeNode) error
	visit = func(node *contentTreeNode) error {
		if len(node.Children) != 0 {
			for _, child := range node.Children {
				if err := visit(child); err != nil {
					return err
				}
			}
			return nil
		}
		if node.NodeType == model.ContentNodeTypeDocument {
			return nil
		}
		search := treeNodeSearchText(title, node)
		searchTokens := len(splitter.tp.Encode(search, nil, nil))
		if node.NodeType == model.ContentNodeTypeAggregate && searchTokens > profile.MaxTokens {
			return fmt.Errorf("aggregate exceeds retrieval token budget: %d > %d", searchTokens, profile.MaxTokens)
		}
		if searchTokens <= profile.MaxTokens {
			result = append(result, generatedChunk{leaf: node, content: node.Text, start: node.SelfRange.Start, end: node.SelfRange.End, searchText: search})
			return nil
		}
		for start := 0; start < len(node.Text); {
			end, err := largestBudgetedRuneEnd(node, title, start, splitter, profile)
			if err != nil {
				return err
			}
			part := node.Text[start:end]
			probe := *node
			probe.Text = part
			end = preferredChunkEnd(node.Text, start, end)
			part = node.Text[start:end]
			probe.Text = part
			if part == "" {
				return fmt.Errorf("tokenizer produced empty oversized-leaf part")
			}
			if len(splitter.tp.Encode(treeNodeSearchText(title, &probe), nil, nil)) > profile.MaxTokens {
				return fmt.Errorf("rune-aligned chunk exceeds retrieval token budget")
			}
			absoluteStart := node.SelfRange.Start + start
			absoluteEnd := node.SelfRange.Start + end
			result = append(result, generatedChunk{leaf: node, content: part, start: absoluteStart, end: absoluteEnd, searchText: treeNodeSearchText(title, &probe)})
			if end == len(node.Text) {
				break
			}
			nextStart := overlapRuneStart(node.Text, start, end, profile.OverlapTokens, splitter)
			if nextStart <= start {
				applogger.Warn("KB chunk overlap skipped because rune-aligned split left no forward progress", "start_offset", start, "end_offset", end, "overlap_tokens", profile.OverlapTokens)
				nextStart = end
			}
			start = nextStart
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	return result, nil
}

// preferredChunkEnd keeps an already budget-safe endpoint whenever possible,
// but moves it back to a nearby paragraph, sentence, or whitespace boundary.
// It only selects UTF-8 rune boundaries, so source ranges remain reproducible.
func largestBudgetedRuneEnd(node *contentTreeNode, title string, start int, splitter *textSplitter, profile retrievalTokenProfile) (int, error) {
	boundaries := runeBoundaries(node.Text, start, len(node.Text))
	low, high, best := 0, len(boundaries)-1, -1
	for low <= high {
		middle := low + (high-low)/2
		probe := *node
		probe.Text = node.Text[start:boundaries[middle]]
		if len(splitter.tp.Encode(treeNodeSearchText(title, &probe), nil, nil)) <= profile.MaxTokens {
			best = middle
			low = middle + 1
			continue
		}
		high = middle - 1
	}
	if best < 0 || boundaries[best] <= start {
		return 0, fmt.Errorf("structure context leaves no rune that fits retrieval token budget")
	}
	return boundaries[best], nil
}

func preferredChunkEnd(text string, start, safeEnd int) int {
	bestSentence := 0
	bestWhitespace := 0
	for _, end := range runeBoundaries(text, start, safeEnd) {
		if end <= start {
			continue
		}
		part := text[start:end]
		if strings.HasSuffix(part, "\n\n") || strings.HasSuffix(part, "\n") {
			bestSentence = end
			continue
		}
		last, _ := utf8.DecodeLastRuneInString(part)
		if strings.ContainsRune(".?!。！？；;", last) {
			bestSentence = end
		}
		if unicode.IsSpace(last) {
			bestWhitespace = end
		}
	}
	if bestSentence != 0 {
		return bestSentence
	}
	if bestWhitespace != 0 {
		return bestWhitespace
	}
	return safeEnd
}

// overlapRuneStart retains approximately the configured number of tokenizer
// tokens while keeping the next source range at a UTF-8 rune boundary.
func overlapRuneStart(text string, start, end, overlapTokens int, splitter *textSplitter) int {
	if overlapTokens == 0 {
		return end
	}
	boundaries := runeBoundaries(text, start, end)
	for index := len(boundaries) - 2; index >= 0; index-- {
		candidate := boundaries[index]
		if len(splitter.tp.Encode(text[candidate:end], nil, nil)) >= overlapTokens {
			return candidate
		}
	}
	return end
}

func runeBoundaries(text string, start, end int) []int {
	boundaries := []int{start}
	for index := start; index < end; {
		_, width := utf8.DecodeRuneInString(text[index:end])
		if width == 0 {
			break
		}
		index += width
		boundaries = append(boundaries, index)
	}
	return boundaries
}
