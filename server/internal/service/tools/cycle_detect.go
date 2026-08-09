// Package tools provides reusable, path-based tool implementations for agent
// operations. Tools accept directory paths directly (not person/session IDs)
// and are free of domain-specific interfaces like Tool/ToolName/Schema.
//
// Higher-level packages (task/tools, privatespace/tools) wrap these cores with
// their own interface contracts.
package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// CycleStatus carries the result of cycle detection after a tool call.
type CycleStatus struct {
	// Warning is appended to the tool result when a cyclical pattern is detected.
	Warning string
	// Blocked triggers a forced checkpoint at the next iteration.
	Blocked bool
	// Reason is a human-readable block reason.
	Reason string
}

// NoCycleDetected is the zero-value CycleStatus, returned when no cycle is detected.
var NoCycleDetected = CycleStatus{}

// Cycle detection thresholds.
const (
	warnThreshold  = 3
	blockThreshold = 8
)

// CycleDetector tracks whether a tool receives the same input and produces
// the same output repeatedly. Same (args, result) pair repeating N times
// consecutively is a cycle.
//
// Tools embed this struct. The CycleDetect method is promoted to the
// embedding tool. Zero-value is immediately usable.
type CycleDetector struct {
	lastSignature string
	count         int
	blocked       bool
	blockReason   string
}

// CycleDetect checks whether the current (args, result) pair matches the
// previous one. Consecutive identical pairs increment the counter;
// any difference resets it.
func (d *CycleDetector) CycleDetect(args map[string]interface{}, result string) CycleStatus {
	if d.blocked {
		return CycleStatus{Blocked: true, Reason: d.blockReason}
	}

	sig := signature(args, result)
	if sig == d.lastSignature {
		d.count++
	} else {
		d.count = 0
	}
	d.lastSignature = sig

	if d.count >= blockThreshold {
		d.blocked = true
		d.blockReason = fmt.Sprintf("same call repeated %d times", d.count)
		return CycleStatus{Blocked: true, Reason: d.blockReason}
	}

	if d.count >= warnThreshold {
		return CycleStatus{
			Warning: formatWarning(d.count),
		}
	}

	return NoCycleDetected
}

func signature(args map[string]interface{}, result string) string {
	normalizedArgs, _ := json.Marshal(args)
	return hashString(string(normalizedArgs) + ":" + result)
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func formatWarning(count int) string {
	return fmt.Sprintf(
		"[Loop Warning: repeated_call; count=%d]\n"+
			"The same tool call has produced the same result %d times in a row.\n"+
			"Ask yourself: Is repeating this making progress? Could a different\n"+
			"approach work? Perhaps the root cause is elsewhere.",
		count, count,
	)
}
