package tools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"unicode/utf8"
)

const (
	defaultReadLines   = 200
	maxReadLines       = 500
	defaultReadBytes   = 8192
	maxReadBytes       = 12288
	maxTextResultBytes = 16 * 1024
)

// TextFileTools provides bounded text operations within one Agent Owned Space.
type TextFileTools struct {
	rootDir string
	workDir string
}

// NewTextFileTools binds file operations to a resource root and working directory.
func NewTextFileTools(rootDir, workDir string) *TextFileTools {
	return &TextFileTools{rootDir: rootDir, workDir: workDir}
}

type textLine struct {
	LineNumber             int    `json:"line_number"`
	Content                string `json:"content"`
	StartByteOffset        int64  `json:"start_byte_offset"`
	EndByteOffsetExclusive int64  `json:"end_byte_offset_exclusive"`
}

type textLinesResult struct {
	Lines                     []textLine `json:"lines"`
	LastReturnedLineTruncated bool       `json:"last_returned_line_truncated"`
	EOF                       bool       `json:"eof"`
	FileSizeBytes             int64      `json:"file_size_bytes"`
}

type textBlockResult struct {
	Content         string `json:"content"`
	StartByteOffset int64  `json:"start_byte_offset"`
	NextByteOffset  int64  `json:"next_byte_offset"`
	EOF             bool   `json:"eof"`
	FileSizeBytes   int64  `json:"file_size_bytes"`
}

// openTextFile applies the existing path boundary and binary screen without
// loading the file into memory. The caller owns the returned descriptor.
func (t *TextFileTools) openTextFile(args map[string]interface{}) (*os.File, int64, error) {
	filePath, _ := args["file_path"].(string)
	if filePath == "" {
		return nil, 0, fmt.Errorf("file_path is required")
	}
	absPath, err := ResolvePath(filePath, t.rootDir, t.workDir)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve path: %w", err)
	}
	f, err := os.Open(absPath)
	if err != nil {
		return nil, 0, fmt.Errorf("open file %q: %w", filePath, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, 0, fmt.Errorf("path is not a regular file: %s", filePath)
	}
	if info.Size() > maxFileBytes {
		f.Close()
		return nil, 0, fmt.Errorf("file is too large (%d bytes, max %d)", info.Size(), maxFileBytes)
	}
	var head [binarySniffBytes]byte
	n, err := f.ReadAt(head[:], 0)
	if err != nil && err != io.EOF {
		f.Close()
		return nil, 0, fmt.Errorf("inspect file: %w", err)
	}
	if IsBinaryFile(head[:n], absPath) {
		f.Close()
		return nil, 0, fmt.Errorf("binary file detected; text tools only support text files")
	}
	return f, info.Size(), nil
}

// textIntArg rejects invalid positions and budgets rather than silently moving them.
func textIntArg(args map[string]interface{}, key string, fallback, minimum, maximum int64) (int64, error) {
	v, ok := args[key]
	if !ok {
		return fallback, nil
	}
	var n int64
	switch x := v.(type) {
	case float64:
		if x < float64(minimum) || x > float64(maximum) || x != float64(int64(x)) {
			return 0, fmt.Errorf("%s must be an integer between %d and %d", key, minimum, maximum)
		}
		n = int64(x)
	case int:
		n = int64(x)
	case int64:
		n = x
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if n < minimum || n > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", key, minimum, maximum)
	}
	return n, nil
}

// byteBoundary checks a source offset without validating the file's contents.
func byteBoundary(f *os.File, offset, size int64) error {
	if offset < 0 || offset > size {
		return fmt.Errorf("byte offset %d is outside file size %d", offset, size)
	}
	if offset == size {
		return nil
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], offset); err != nil {
		return fmt.Errorf("read byte offset: %w", err)
	}
	if b[0]&0xc0 == 0x80 {
		return fmt.Errorf("byte offset %d is inside a UTF-8 character", offset)
	}
	return nil
}

func jsonText(v interface{}) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if len(b) > maxTextResultBytes {
		return "", fmt.Errorf("text tool result exceeds %d bytes", maxTextResultBytes)
	}
	return string(b), nil
}

// nextTextRune peeks one complete UTF-8 rune; invalid bytes are still consumed
// individually and JSON marshaling presents them with replacement characters.
func nextTextRune(r *bufio.Reader) ([]byte, error) {
	first, err := r.Peek(1)
	if err != nil {
		return nil, err
	}
	width := utf8.RuneLen(rune(first[0]))
	if first[0] >= utf8.RuneSelf {
		width = 1
		if first[0]&0xe0 == 0xc0 {
			width = 2
		} else if first[0]&0xf0 == 0xe0 {
			width = 3
		} else if first[0]&0xf8 == 0xf0 {
			width = 4
		}
	}
	b, err := r.Peek(width)
	if err == io.EOF {
		b, _ = r.Peek(1)
	} else if err != nil {
		return nil, err
	}
	if !utf8.Valid(b) {
		b = b[:1]
	}
	return b, nil
}

// ReadTextLines returns bounded numbered lines. A partial final line carries
// the source offset needed to continue with ReadTextBlock.
func (t *TextFileTools) ReadTextLines(args map[string]interface{}) (string, error) {
	start, err := textIntArg(args, "start_line", 1, 1, 1<<31-1)
	if err != nil {
		return "", err
	}
	limit, err := textIntArg(args, "max_lines", defaultReadLines, 1, maxReadLines)
	if err != nil {
		return "", err
	}
	budget, err := textIntArg(args, "max_bytes", defaultReadBytes, 1, maxReadBytes)
	if err != nil {
		return "", err
	}
	f, size, err := t.openTextFile(args)
	if err != nil {
		return "", err
	}
	defer f.Close()
	result := textLinesResult{Lines: []textLine{}, FileSizeBytes: size}
	r := bufio.NewReader(f)
	var offset int64
	for line := int64(1); line < start && offset < size; line++ {
		for offset < size {
			part, readErr := r.ReadSlice('\n')
			offset += int64(len(part))
			if readErr == nil {
				break
			}
			if readErr != bufio.ErrBufferFull && readErr != io.EOF {
				return "", readErr
			}
			if readErr == io.EOF {
				break
			}
		}
	}
	if offset == size {
		result.EOF = true
		return jsonText(result)
	}
	for lineNo := start; lineNo < start+limit && offset < size; lineNo++ {
		lineStart := offset
		content := make([]byte, 0, 256)
		probe := result
		probe.Lines = append(append([]textLine{}, result.Lines...), textLine{LineNumber: int(lineNo), StartByteOffset: lineStart, EndByteOffsetExclusive: size})
		base, err := json.Marshal(probe)
		if err != nil {
			return "", err
		}
		room := maxTextResultBytes - len(base)
		if room <= 0 {
			break
		}
		used := 0
		completeLine := false
		for offset < size && budget > 0 {
			b, readErr := nextTextRune(r)
			if readErr != nil {
				return "", readErr
			}
			encoded, _ := json.Marshal(string(b))
			cost := len(encoded) - 2
			if int64(len(b)) > budget || cost > room-used {
				break
			}
			if _, err := r.Discard(len(b)); err != nil {
				return "", err
			}
			content = append(content, b...)
			used += cost
			offset += int64(len(b))
			budget -= int64(len(b))
			if b[0] == '\n' {
				completeLine = true
				break
			}
		}
		if len(content) == 0 {
			break
		}
		result.Lines = append(result.Lines, textLine{int(lineNo), string(content), lineStart, offset})
		if !completeLine && offset < size {
			result.LastReturnedLineTruncated = true
			break
		}
	}
	result.EOF = offset == size
	return jsonText(result)
}

// ReadTextBlock returns a bounded source-byte slice ending on a rune boundary.
func (t *TextFileTools) ReadTextBlock(args map[string]interface{}) (string, error) {
	start, err := textIntArg(args, "start_byte_offset", 0, 0, maxFileBytes)
	if err != nil {
		return "", err
	}
	budget, err := textIntArg(args, "max_bytes", defaultReadBytes, 1, maxReadBytes)
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
	if budget > size-start {
		budget = size - start
	}
	buf := make([]byte, budget)
	n, err := f.ReadAt(buf, start)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read block: %w", err)
	}
	buf = buf[:n]
	for len(buf) > 0 && incompleteSuffix(buf) {
		buf = buf[:len(buf)-1]
	}
	result := textBlockResult{StartByteOffset: start, NextByteOffset: size, FileSizeBytes: size}
	base, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	room := maxTextResultBytes - len(base)
	used, end := 0, 0
	for end < len(buf) {
		_, width := utf8.DecodeRune(buf[end:])
		encoded, err := json.Marshal(string(buf[end : end+width]))
		if err != nil {
			return "", err
		}
		cost := len(encoded) - 2
		if used+cost > room {
			break
		}
		used += cost
		end += width
	}
	result.Content = string(buf[:end])
	result.NextByteOffset = start + int64(end)
	result.EOF = result.NextByteOffset == size
	return jsonText(result)
}

func incompleteSuffix(data []byte) bool {
	for n := 1; n <= 3 && n <= len(data); n++ {
		part := data[len(data)-n:]
		if part[0]&0xc0 == 0xc0 && !utf8.FullRune(part) {
			return true
		}
	}
	return false
}
