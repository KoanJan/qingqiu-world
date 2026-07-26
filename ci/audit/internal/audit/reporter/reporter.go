// Package reporter formats and outputs audit findings in various formats.
// Supports both human-readable terminal output and machine-readable JSON.
package reporter

import (
	"qingqiu-world-ci/audit/internal/audit/checker"
)

// SummaryStats holds aggregated statistics about audit findings.
type SummaryStats struct {
	// ByCheck maps violation type to count.
	ByCheck map[checker.CheckType]int `json:"by_check"`
	// ByModule maps module path to count.
	ByModule map[string]int `json:"by_module"`
	// BySeverity maps severity level to count.
	BySeverity map[checker.Severity]int `json:"by_severity"`
}

// Report represents the result of a single audit scan execution.
type Report struct {
	// CreatedAt is the ISO 8601 timestamp of the audit execution.
	CreatedAt string `json:"created_at"`
	// Version is the tool version that produced the report.
	Version string `json:"version"`
	// TotalFiles is the number of source files scanned.
	TotalFiles int `json:"total_files"`
	// FindingCount is the total number of violations found.
	FindingCount int `json:"finding_count"`
	// Findings contains all violations detected during the scan.
	Findings []checker.Finding `json:"findings"`
	// Summary holds aggregated statistics.
	Summary SummaryStats `json:"summary"`
}

// Reporter defines the interface for output formatting.
type Reporter interface {
	// Report formats and outputs the audit report.
	Report(r *Report) error
}

// ComputeSummary calculates SummaryStats from a slice of findings.
func ComputeSummary(findings []checker.Finding) SummaryStats {
	s := SummaryStats{
		ByCheck:    make(map[checker.CheckType]int),
		ByModule:   make(map[string]int),
		BySeverity: make(map[checker.Severity]int),
	}
	for _, f := range findings {
		s.ByCheck[f.Check]++
		s.BySeverity[f.Severity]++

		// Extract module from file path (first two path segments)
		module := extractModule(f.File)
		s.ByModule[module]++
	}
	return s
}

// extractModule returns the top-level module name from a file path.
// For example, "server/internal/service/memory/memory.go" → "server/internal/service/memory".
func extractModule(filePath string) string {
	// Split into components and take the directory portion.
	parts := []string{}
	for _, p := range splitPath(filePath) {
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return filePath
	}
	// Return directory part (all but the filename)
	if len(parts) == 1 {
		return parts[0]
	}
	return joinPath(parts[:len(parts)-1]...)
}

// splitPath splits a path by separator, filtering empty segments.
func splitPath(p string) []string {
	var parts []string
	current := ""
	for _, ch := range p {
		if ch == '/' || ch == '\\' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(ch)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

// joinPath joins path segments with '/'.
func joinPath(parts ...string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "/"
		}
		result += p
	}
	return result
}
