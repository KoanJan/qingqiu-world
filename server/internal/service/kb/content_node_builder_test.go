package kb

import (
	"strings"
	"testing"

	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/kb/format"
)

func TestBuildContentTreeCreatesHeadingSubtrees(t *testing.T) {
	source := "# First\n\nfirst body\n\n## Nested\n\nnested body\n\n# Second\n\nsecond body\n"
	adapter, _ := format.Select("md")
	parsed, err := adapter.Parse(format.Document{Text: source, FileType: "md"})
	blocks := parsedBlocksFromFormat(parsed)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	profile := testProfile()
	profile.MinTokens = 1
	profile.MaxTokens = 2
	tree, err := buildContentTree("Guide", source, blocks, profile, wordTokens)
	if err != nil {
		t.Fatalf("buildContentTree() error = %v", err)
	}
	if len(tree.Children) != 2 {
		t.Fatalf("root child count = %d, want 2 headings", len(tree.Children))
	}
	first := tree.Children[0]
	if first.NodeType != model.ContentNodeTypeHeading || len(first.Children) != 2 {
		t.Fatalf("first heading structure = type %d children %d, want heading with body and nested heading", first.NodeType, len(first.Children))
	}
	if first.SubtreeRange.End <= first.SelfRange.End {
		t.Fatalf("heading subtree [%d,%d) must include descendants beyond self [%d,%d)", first.SubtreeRange.Start, first.SubtreeRange.End, first.SelfRange.Start, first.SelfRange.End)
	}
}

func TestBuildContentTreeRepairsShortAggregateTail(t *testing.T) {
	source := "this is a sufficiently long content entry\none\ntwo\nthree\n"
	firstEnd := strings.Index(source, "\none\n") + 1
	blocks := []parsedBlock{
		{NodeType: model.ContentNodeTypeParagraph, Text: source[:firstEnd], SelfRange: sourceRange{Start: 0, End: firstEnd}, SubtreeRange: sourceRange{Start: 0, End: firstEnd}},
		{NodeType: model.ContentNodeTypeParagraph, Text: source[firstEnd : firstEnd+4], SelfRange: sourceRange{Start: firstEnd, End: firstEnd + 4}, SubtreeRange: sourceRange{Start: firstEnd, End: firstEnd + 4}},
		{NodeType: model.ContentNodeTypeParagraph, Text: source[firstEnd+4 : firstEnd+8], SelfRange: sourceRange{Start: firstEnd + 4, End: firstEnd + 8}, SubtreeRange: sourceRange{Start: firstEnd + 4, End: firstEnd + 8}},
		{NodeType: model.ContentNodeTypeParagraph, Text: source[firstEnd+8:], SelfRange: sourceRange{Start: firstEnd + 8, End: len(source)}, SubtreeRange: sourceRange{Start: firstEnd + 8, End: len(source)}},
	}
	profile := testProfile()
	profile.MinTokens = 12
	profile.MaxTokens = 20
	count := func(text string) int {
		if strings.Contains(text, "LeafType: 6") {
			return 100 // Force the structural path instead of DocumentBody fast path.
		}
		return wordTokens(text)
	}
	tree, err := buildContentTree("T", source, blocks, profile, count)
	if err != nil {
		t.Fatalf("buildContentTree() error = %v", err)
	}
	if len(tree.Children) != 2 || tree.Children[1].NodeType != model.ContentNodeTypeAggregate {
		t.Fatalf("short sequence should produce a repaired aggregate, got %#v", tree.Children)
	}
	if got, want := tree.Children[1].Text, source[firstEnd:]; got != want {
		t.Fatalf("aggregate source = %q, want %q", got, want)
	}
}

func TestBuildContentTreeUsesDocumentBodyForSmallDocument(t *testing.T) {
	source := "- name: Ada\n- role: engineer\n"
	adapter, _ := format.Select("md")
	parsed, err := adapter.Parse(format.Document{Text: source, FileType: "md"})
	blocks := parsedBlocksFromFormat(parsed)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	profile := testProfile()
	profile.MaxTokens = 100
	tree, err := buildContentTree("Profile", source, blocks, profile, wordTokens)
	if err != nil {
		t.Fatalf("buildContentTree() error = %v", err)
	}
	if len(tree.Children) != 1 || tree.Children[0].NodeType != model.ContentNodeTypeDocumentBody {
		t.Fatalf("small document must be one document body leaf")
	}
}

func TestTreeNodeSearchTextIncludesStableStructuralMetadata(t *testing.T) {
	root := &contentTreeNode{NodeType: model.ContentNodeTypeDocument}
	heading := &contentTreeNode{NodeType: model.ContentNodeTypeHeading, Text: "Overview", Parent: root}
	leaf := &contentTreeNode{NodeType: model.ContentNodeTypeListItem, Text: "Ada", SelfRange: sourceRange{Start: 12, End: 15}, Parent: heading}
	encoded := treeNodeSearchText("Profile", leaf)
	for _, expected := range []string{"Document: Profile", "Structure: Profile > Overview", "LeafType: 9", "SourceRange: 12-15", "Ada"} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("SearchText = %q, missing %q", encoded, expected)
		}
	}
}

func testProfile() retrievalTokenProfile {
	return retrievalTokenProfile{MinTokens: 4, MaxTokens: 20, OverlapTokens: 0, EmbeddingMaxLen: 100}
}

func wordTokens(text string) int {
	return len(strings.Fields(text))
}
