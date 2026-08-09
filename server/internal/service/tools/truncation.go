package tools

import (
	"fmt"
	"strings"
)

const (
	DefaultTruncateBytes = 20 * 1024 // 20KB
	newlineAlignWindow   = 1024      // Line-boundary alignment window (1KB)
)

// TruncateHead keeps the beginning of s and truncates the tail.
// Aligns to a line boundary within a 1KB window near the cut point
// to avoid splitting a line in half.
func TruncateHead(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	cut := maxBytes
	if lastNL := strings.LastIndex(s[:maxBytes], "\n"); lastNL > maxBytes-newlineAlignWindow {
		cut = lastNL
	}
	return s[:cut], true
}

// TruncateTail keeps the end of s and truncates the head.
// Used by bash and similar tools where the end is more important.
func TruncateTail(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	start := len(s) - maxBytes
	if start < 0 {
		start = 0
	}
	if firstNL := strings.Index(s[start:], "\n"); firstNL >= 0 && firstNL < newlineAlignWindow {
		start = start + firstNL + 1
	}
	return s[start:], true
}

// Hint generates a truncation notice showing how many bytes were retained.
func Hint(shown, total int) string {
	return fmt.Sprintf("[truncated: showed %d of %d bytes]", shown, total)
}
