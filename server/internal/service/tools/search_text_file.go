package tools

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"unicode/utf8"
)

const (
	defaultSearchMatches      = 10
	maxSearchMatches          = 20
	maxSearchQueryBytes       = 1024
	searchSnippetContextBytes = 120
)

type textMatch struct {
	StartByteOffset int64  `json:"start_byte_offset"`
	Snippet         string `json:"snippet"`
}

type textSearchResult struct {
	Matches        []textMatch `json:"matches"`
	NextByteOffset int64       `json:"next_byte_offset"`
	Complete       bool        `json:"complete"`
	FileSizeBytes  int64       `json:"file_size_bytes"`
}

// SearchTextFile finds literal, case-sensitive UTF-8 text in one file. The
// returned cursor points to the first unreturned match, allowing direct seek.
func (t *TextFileTools) SearchTextFile(args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	if len(query) == 0 || len(query) > maxSearchQueryBytes || !utf8.ValidString(query) {
		return "", fmt.Errorf("query must be nonempty UTF-8 text of at most %d bytes", maxSearchQueryBytes)
	}
	start, err := textIntArg(args, "start_byte_offset", 0, 0, maxFileBytes)
	if err != nil {
		return "", err
	}
	limit, err := textIntArg(args, "max_matches", defaultSearchMatches, 1, maxSearchMatches)
	if err != nil {
		return "", err
	}
	f, size, err := t.openTextFile(args)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := byteBoundary(f, start, size); err != nil {
		return "", err
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	const scanBufferBytes = 64 * 1024
	r := bufio.NewReaderSize(f, scanBufferBytes)
	needle := []byte(query)
	result := textSearchResult{Matches: []textMatch{}, NextByteOffset: size, Complete: true, FileSizeBytes: size}
	for offset := start; offset+int64(len(needle)) <= size; {
		window, err := r.Peek(scanBufferBytes)
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("scan file: %w", err)
		}
		// Keep query-length minus one source bytes when advancing a chunk;
		// this preserves matches spanning the buffered read boundary.
		candidates := len(window) - len(needle) + 1
		if candidates <= 0 {
			break
		}
		index := bytes.Index(window[:candidates+len(needle)-1], needle)
		if index < 0 {
			if _, err := r.Discard(candidates); err != nil {
				return "", err
			}
			offset += int64(candidates)
			continue
		}
		offset += int64(index)
		if _, err := r.Discard(index); err != nil {
			return "", err
		}
		{
			if int64(len(result.Matches)) == limit {
				result.Complete = false
				result.NextByteOffset = offset
				return jsonText(result)
			}
			snippet, err := readMatchSnippet(f, offset, int64(len(needle)), size)
			if err != nil {
				return "", err
			}
			candidate := append(result.Matches, textMatch{offset, snippet})
			probe := result
			probe.Matches = candidate
			probe.Complete = false
			probe.NextByteOffset = offset + 1
			encoded, err := json.Marshal(probe)
			if err != nil {
				return "", err
			}
			if len(encoded) > maxTextResultBytes {
				if len(result.Matches) == 0 {
					return "", fmt.Errorf("one search match exceeds output limit")
				}
				result.Complete = false
				result.NextByteOffset = offset
				return jsonText(result)
			}
			result.Matches = candidate
		}
		if _, err := r.Discard(1); err != nil {
			return "", err
		}
		offset++
	}
	return jsonText(result)
}

// readMatchSnippet reads only the match and bounded neighboring source bytes.
func readMatchSnippet(f *os.File, start, queryBytes, size int64) (string, error) {
	begin := start - searchSnippetContextBytes
	if begin < 0 {
		begin = 0
	}
	end := start + queryBytes + searchSnippetContextBytes
	if end > size {
		end = size
	}
	buf := make([]byte, end-begin)
	if _, err := f.ReadAt(buf, begin); err != nil && err != io.EOF {
		return "", fmt.Errorf("read search snippet: %w", err)
	}
	for len(buf) > 0 && buf[0]&0xc0 == 0x80 {
		buf = buf[1:]
	}
	for len(buf) > 0 && incompleteSuffix(buf) {
		buf = buf[:len(buf)-1]
	}
	return string(buf), nil
}
