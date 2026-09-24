package kb

import (
	"strings"
	"testing"
	"unicode/utf8"

	"qingqiu-world-server/internal/model"
)

func TestGenerateChunksRejectsOversizedAggregate(t *testing.T) {
	splitter := newTextSplitter(32, 0, 4)
	if err := splitter.Err(); err != nil {
		t.Fatalf("tokenizer initialization failed: %v", err)
	}
	text := strings.Repeat("short item ", 80)
	root := &contentTreeNode{NodeType: model.ContentNodeTypeDocument, Children: []*contentTreeNode{{NodeType: model.ContentNodeTypeAggregate, Text: text, SelfRange: sourceRange{Start: 0, End: len(text)}, SubtreeRange: sourceRange{Start: 0, End: len(text)}}}}
	root.Children[0].Parent = root
	_, err := generateChunks(root, "Test", splitter, retrievalTokenProfile{MinTokens: 4, MaxTokens: 32, OverlapTokens: 0, EmbeddingMaxLen: 128})
	if err == nil || !strings.Contains(err.Error(), "aggregate exceeds") {
		t.Fatalf("oversized aggregate error = %v, want budget failure", err)
	}
}

func TestGenerateChunksKeepsUTF8SourceProvenance(t *testing.T) {
	splitter := newTextSplitter(40, 8, 4)
	if err := splitter.Err(); err != nil {
		t.Fatalf("tokenizer initialization failed: %v", err)
	}
	text := strings.Repeat("第一句内容用于测试中文切分。第二句继续说明。\n", 24)
	root := &contentTreeNode{NodeType: model.ContentNodeTypeDocument}
	leaf := &contentTreeNode{NodeType: model.ContentNodeTypeParagraph, Text: text, SelfRange: sourceRange{Start: 0, End: len(text)}, SubtreeRange: sourceRange{Start: 0, End: len(text)}, Parent: root}
	root.Children = []*contentTreeNode{leaf}
	profile := retrievalTokenProfile{MinTokens: 4, MaxTokens: 40, OverlapTokens: 8, EmbeddingMaxLen: 128}
	chunks, err := generateChunks(root, "测试文档", splitter, profile)
	if err != nil {
		t.Fatalf("generateChunks() error = %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("chunk count = %d, want split oversized leaf", len(chunks))
	}
	for index, chunk := range chunks {
		if !utf8.ValidString(chunk.content) {
			t.Fatalf("chunk %d is not valid UTF-8", index)
		}
		if got := text[chunk.start:chunk.end]; got != chunk.content {
			t.Fatalf("chunk %d source = %q, content = %q", index, got, chunk.content)
		}
		if tokens := len(splitter.tp.Encode(chunk.searchText, nil, nil)); tokens > profile.MaxTokens {
			t.Fatalf("chunk %d search tokens = %d, max = %d", index, tokens, profile.MaxTokens)
		}
	}
}
