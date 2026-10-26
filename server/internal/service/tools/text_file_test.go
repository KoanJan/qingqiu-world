package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func textFixture(t *testing.T, content string) (*TextFileTools, string) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "example.txt"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return NewTextFileTools(root, root), "example.txt"
}

func decodeTextResult(t *testing.T, value string, target interface{}) {
	t.Helper()
	if len(value) > maxTextResultBytes {
		t.Fatalf("result is %d bytes", len(value))
	}
	if err := json.Unmarshal([]byte(value), target); err != nil {
		t.Fatal(err)
	}
}

func TestTextLinesPartialLineAndBlockContinuation(t *testing.T) {
	core, path := textFixture(t, "one\ntwo\nthree\nfour\nfive\nabcdefghijklmnopqrstuvwxyz\nseven\n")
	value, err := core.ReadTextLines(map[string]interface{}{"file_path": path, "start_line": float64(3), "max_lines": float64(8), "max_bytes": float64(24)})
	if err != nil {
		t.Fatal(err)
	}
	var page textLinesResult
	decodeTextResult(t, value, &page)
	if len(page.Lines) != 4 || page.Lines[3].Content != "abcdefgh" || page.Lines[3].StartByteOffset != 24 || page.Lines[3].EndByteOffsetExclusive != 32 || !page.LastReturnedLineTruncated || page.EOF {
		t.Fatalf("unexpected page: %+v", page)
	}
	value, err = core.ReadTextBlock(map[string]interface{}{"file_path": path, "start_byte_offset": float64(32), "max_bytes": float64(19)})
	if err != nil {
		t.Fatal(err)
	}
	var block textBlockResult
	decodeTextResult(t, value, &block)
	if block.Content != "ijklmnopqrstuvwxyz\n" || block.NextByteOffset != 51 || block.EOF {
		t.Fatalf("unexpected block: %+v", block)
	}
	value, err = core.ReadTextLines(map[string]interface{}{"file_path": path, "start_line": float64(7)})
	if err != nil {
		t.Fatal(err)
	}
	decodeTextResult(t, value, &page)
	if len(page.Lines) != 1 || page.Lines[0].Content != "seven\n" || !page.EOF {
		t.Fatalf("unexpected last page: %+v", page)
	}
}

func TestLongLineAndUnicodeBoundaries(t *testing.T) {
	core, path := textFixture(t, strings.Repeat("<article>🍰", 30000)+"\n")
	value, err := core.ReadTextLines(map[string]interface{}{"file_path": path})
	if err != nil {
		t.Fatal(err)
	}
	var page textLinesResult
	decodeTextResult(t, value, &page)
	if len(page.Lines) != 1 || !page.LastReturnedLineTruncated || page.EOF {
		t.Fatalf("unexpected long-line page: %+v", page)
	}
	if len(page.Lines[0].Content) > defaultReadBytes {
		t.Fatal("raw byte budget exceeded")
	}
	value, err = core.ReadTextBlock(map[string]interface{}{"file_path": path, "start_byte_offset": float64(page.Lines[0].EndByteOffsetExclusive), "max_bytes": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	var block textBlockResult
	decodeTextResult(t, value, &block)
	if block.NextByteOffset < page.Lines[0].EndByteOffsetExclusive || block.EOF {
		t.Fatalf("bad continuation: %+v", block)
	}
	_, err = core.ReadTextBlock(map[string]interface{}{"file_path": path, "start_byte_offset": float64(10)})
	if err == nil {
		t.Fatal("offset inside emoji should fail")
	}
}

func TestSearchPagesOverlappingAndChunkBoundary(t *testing.T) {
	core, path := textFixture(t, strings.Repeat("x", 65534)+"ababa"+strings.Repeat("y", 10000)+"aba")
	args := map[string]interface{}{"file_path": path, "query": "aba", "max_matches": float64(1)}
	var offsets []int64
	for {
		value, err := core.SearchTextFile(args)
		if err != nil {
			t.Fatal(err)
		}
		var page textSearchResult
		decodeTextResult(t, value, &page)
		for _, match := range page.Matches {
			offsets = append(offsets, match.StartByteOffset)
		}
		if page.Complete {
			break
		}
		args["start_byte_offset"] = float64(page.NextByteOffset)
	}
	if len(offsets) != 3 || offsets[0] != 65534 || offsets[1] != 65536 || offsets[2] != 75539 {
		t.Fatalf("wrong match offsets: %v", offsets)
	}
	value, err := core.SearchTextFile(map[string]interface{}{"file_path": path, "query": "missing"})
	if err != nil {
		t.Fatal(err)
	}
	var page textSearchResult
	decodeTextResult(t, value, &page)
	if !page.Complete || len(page.Matches) != 0 {
		t.Fatalf("missing search: %+v", page)
	}
}

func TestEscapedOutputBudgetAndLargeFile(t *testing.T) {
	core, path := textFixture(t, strings.Repeat("\t\"\\", 5000))
	value, err := core.ReadTextLines(map[string]interface{}{"file_path": path, "max_bytes": float64(12288)})
	if err != nil {
		t.Fatal(err)
	}
	var lines textLinesResult
	decodeTextResult(t, value, &lines)
	if !lines.LastReturnedLineTruncated {
		t.Fatal("escaped long line should be truncated")
	}
	value, err = core.ReadTextBlock(map[string]interface{}{"file_path": path, "max_bytes": float64(12288)})
	if err != nil {
		t.Fatal(err)
	}
	var block textBlockResult
	decodeTextResult(t, value, &block)
	if block.NextByteOffset == 0 || block.EOF {
		t.Fatalf("unexpected escaped block: %+v", block)
	}
	// A text file above the former 10 MiB limit still supports direct bounded reads.
	if err := os.WriteFile(filepath.Join(core.rootDir, "large.txt"), []byte(strings.Repeat("x", 11*1024*1024-3)+"END"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err = core.ReadTextBlock(map[string]interface{}{"file_path": "large.txt", "start_byte_offset": float64(11*1024*1024 - 3)})
	if err != nil {
		t.Fatal(err)
	}
	decodeTextResult(t, value, &block)
	if block.Content != "END" || !block.EOF {
		t.Fatalf("large-file tail: %+v", block)
	}
}

func TestTextFileBoundaries(t *testing.T) {
	core, path := textFixture(t, "é\n")
	value, err := core.ReadTextBlock(map[string]interface{}{"file_path": path, "max_bytes": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	var block textBlockResult
	decodeTextResult(t, value, &block)
	if block.Content != "" || block.EOF || block.NextByteOffset != 0 {
		t.Fatalf("small-byte result: %+v", block)
	}
	if _, err := core.ReadTextLines(map[string]interface{}{"file_path": "../outside"}); err == nil {
		t.Fatal("escaped path accepted")
	}
	if _, err := core.ReadTextBlock(map[string]interface{}{"file_path": path, "start_byte_offset": float64(1)}); err == nil {
		t.Fatal("offset inside rune accepted")
	}
	if _, err := core.SearchTextFile(map[string]interface{}{"file_path": path, "query": "é", "start_byte_offset": float64(1)}); err == nil {
		t.Fatal("search offset inside rune accepted")
	}
	root := core.rootDir
	if err := os.WriteFile(filepath.Join(root, "binary.txt"), []byte{'x', 0, 'y'}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := core.ReadTextLines(map[string]interface{}{"file_path": "binary.txt"}); err == nil {
		t.Fatal("binary file accepted")
	}
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := core.SearchTextFile(map[string]interface{}{"file_path": "link.txt", "query": "secret"}); err == nil {
		t.Fatal("out-of-root symlink accepted")
	}
}
