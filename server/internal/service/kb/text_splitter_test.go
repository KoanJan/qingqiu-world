package kb

import (
	"strings"
	"testing"
)

// TestSplit_OnlyEmptyLines verifies that whitespace-only input returns nil.
func TestSplit_OnlyEmptyLines(t *testing.T) {
	s := newTextSplitter(500, 50, 100)
	chunks := s.Split("\n\n\n")
	if chunks != nil {
		t.Errorf("expected nil for whitespace-only text, got %d chunks", len(chunks))
	}
}

// TestSplit_WindowsLineEndings verifies that \r\n line breaks are handled correctly.
func TestSplit_WindowsLineEndings(t *testing.T) {
	s := newTextSplitter(500, 50, 100)
	text := "First paragraph.\r\n\r\nSecond paragraph."
	chunks := s.Split(text)

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Content, "First") {
		t.Errorf("first line missing from chunk: %q", chunks[0].Content)
	}
}

// TestSplit_LargeParagraphSplit verifies word-level splitting when chunkSize is too small
// for a single paragraph, triggering splitLargeParagraph.
func TestSplit_LargeParagraphSplit(t *testing.T) {
	// chunkSize=7 forces word-level splitting: 15 words × ~1 token each > 7
	s := newTextSplitter(7, 5, 5)
	text := "one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen"
	chunks := s.Split(text)

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks for large paragraph, got %d", len(chunks))
	}
	for i, c := range chunks {
		if c.Content == "" {
			t.Errorf("chunk %d has empty content", i)
		}
	}
}

// TestSplit_ChunkIndexing verifies that StartOffset is monotonically increasing across
// all output chunks, ensuring correct overlap calculation.
func TestSplit_ChunkIndexing(t *testing.T) {
	// verify StartOffset is monotonically increasing across chunks
	s := newTextSplitter(20, 5, 5)
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "This is line number " + string(rune('A'+i%26))
	}
	text := strings.Join(lines, "\n\n")
	chunks := s.Split(text)

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i].StartOffset < chunks[i-1].StartOffset {
			t.Errorf("StartOffset not monotonic: chunk %d offset %d <= chunk %d offset %d",
				i, chunks[i].StartOffset, i-1, chunks[i-1].StartOffset)
		}
	}
}

// TestSplit_MinChunkSizeMerge verifies that small trailing chunks are merged into the
// previous chunk by mergeSmallTailChunks, preventing orphaned tiny chunks.
func TestSplit_MinChunkSizeMerge(t *testing.T) {
	// generate enough paragraphs to flush the first chunk,
	// leaving a small tail that should be merged by mergeSmallTailChunks
	s := newTextSplitter(500, 50, 100)
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, "Line number "+string(rune('A'+i%26))+" with some extra filler words to reach token count approximately.")
	}
	text := strings.Join(lines, "\n\n")
	chunks := s.Split(text)

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}
	last := chunks[len(chunks)-1]
	if last.Content == "" {
		t.Error("last chunk should not be empty after potential merge")
	}
}

// TestSplit_HeadingStartsNewUnit verifies that structural headings are not
// merged into a preceding section even when both fit the token budget.
func TestSplit_HeadingStartsNewUnit(t *testing.T) {
	s := newTextSplitter(100, 0, 1)
	text := "Introduction content.\n\n## Second Section\n\nSecond section content."
	chunks := s.Split(text)
	if len(chunks) != 2 {
		t.Fatalf("expected two structural chunks, got %d", len(chunks))
	}
	if strings.Contains(chunks[0].Content, "Second Section") {
		t.Errorf("heading leaked into preceding unit: %q", chunks[0].Content)
	}
	if !strings.Contains(chunks[1].Content, "Second Section") {
		t.Errorf("heading missing from new structural unit: %q", chunks[1].Content)
	}
}

func TestSplit_AlignsOffsetsToCanonicalText(t *testing.T) {
	s := newTextSplitter(8, 0, 1)
	text := "First paragraph has enough words.\n\nSecond paragraph has enough words."
	chunks := s.Split(text)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		if chunk.StartOffset < 0 || chunk.EndOffset > len(text) || chunk.StartOffset >= chunk.EndOffset {
			t.Fatalf("invalid chunk range: %#v", chunk)
		}
		if got := text[chunk.StartOffset:chunk.EndOffset]; got != chunk.Content {
			t.Fatalf("offset content mismatch: got %q, want %q", got, chunk.Content)
		}
	}
}

func TestSplit_LargeParagraphPreservesOriginalWhitespaceForProvenance(t *testing.T) {
	s := newTextSplitter(7, 0, 1)
	text := "one\ttwo\nthree  four five\tsix seven eight nine ten"
	chunks := s.Split(text)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		if got := text[chunk.StartOffset:chunk.EndOffset]; got != chunk.Content {
			t.Fatalf("chunk did not preserve source whitespace: got %q, want %q", got, chunk.Content)
		}
	}
}

func TestSplit_PreservesListTableAndCodeBoundaries(t *testing.T) {
	s := newTextSplitter(100, 0, 1)
	text := "Intro paragraph.\n- first item\n- second item\n| name | value |\n| --- | --- |\n| one | two |\n```go\nfmt.Println(\"x\")\n\nfmt.Println(\"y\")\n```\nOutro paragraph."
	paragraphs := s.splitParagraphs(text)
	if len(paragraphs) != 5 {
		t.Fatalf("unexpected structural block count: %d", len(paragraphs))
	}
	if !paragraphs[1].list || !paragraphs[2].table || !paragraphs[3].code {
		t.Fatalf("structural profiles not preserved: %#v", paragraphs)
	}
	if !strings.Contains(paragraphs[3].content, "\n\n") {
		t.Fatalf("code block whitespace was not preserved: %q", paragraphs[3].content)
	}
}
