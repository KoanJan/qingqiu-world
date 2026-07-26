// Package reporter: terminal.go formats audit findings as a colored
// human-readable terminal table grouped by module and check type.
package reporter

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"qingqiu-world-ci/audit/internal/audit/checker"
)

// TerminalReporter outputs audit findings to stdout as a formatted table.
type TerminalReporter struct {
	w io.Writer
}

// NewTerminalReporter creates a TerminalReporter writing to stdout.
func NewTerminalReporter() *TerminalReporter {
	return &TerminalReporter{w: os.Stdout}
}

// Report formats and writes the audit report to the terminal.
func (r *TerminalReporter) Report(rep *Report) error {
	r.printHeader(rep)
	r.printSummary(rep)
	r.printDetails(rep)
	return nil
}

// printHeader writes the report title and scan statistics.
func (r *TerminalReporter) printHeader(rep *Report) {
	fmt.Fprintln(r.w, "=== Audit Report ===")
	fmt.Fprintf(r.w, "Scanned: %d files | Violations: %d\n\n", rep.TotalFiles, rep.FindingCount)
}

// printSummary writes violation counts grouped by check type and module.
func (r *TerminalReporter) printSummary(rep *Report) {
	// Print by check type
	fmt.Fprintln(r.w, "By Type:")
	for ct := checker.CheckMissingComment; ct <= checker.CheckDuplicateResource; ct++ {
		if count, ok := rep.Summary.ByCheck[ct]; ok && count > 0 {
			fmt.Fprintf(r.w, "  %-20s %d\n", ct.String(), count)
		}
	}
	fmt.Fprintln(r.w)

	// Print by module (sorted by count descending)
	fmt.Fprintln(r.w, "By Module:")
	type moduleCount struct {
		name  string
		count int
	}
	var modules []moduleCount
	for name, count := range rep.Summary.ByModule {
		modules = append(modules, moduleCount{name, count})
	}
	sort.Slice(modules, func(i, j int) bool {
		return modules[i].count > modules[j].count
	})
	for _, m := range modules {
		fmt.Fprintf(r.w, "  %-40s %d\n", m.name, m.count)
	}
	fmt.Fprintln(r.w)
}

// printDetails writes each finding with file path, line number, type, and message.
func (r *TerminalReporter) printDetails(rep *Report) {
	if len(rep.Findings) == 0 {
		fmt.Fprintln(r.w, "No violations found.")
		return
	}

	fmt.Fprintln(r.w, "Details:")

	// Use tabwriter for aligned columns
	w := tabwriter.NewWriter(r.w, 0, 0, 2, ' ', 0)
	for _, f := range rep.Findings {
		severityLabel := fmt.Sprintf("[%s]", f.Severity.String())
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n",
			f.File,
			fmt.Sprintf("%s:%d", f.Check.String(), f.Line),
			severityLabel,
			f.Message,
		)
	}
	w.Flush()

	// Print suggestions
	fmt.Fprintln(r.w, "\nSuggestions:")
	for i, f := range rep.Findings {
		maxShow := 10
		if i >= maxShow {
			fmt.Fprintf(r.w, "  ... and %d more\n", len(rep.Findings)-maxShow)
			break
		}
		// Only show suggestion if it differs from a generic message
		if f.Suggestion != "" && !strings.HasPrefix(f.Suggestion, "add ") {
			fmt.Fprintf(r.w, "  %s:%d: %s\n", f.File, f.Line, f.Suggestion)
		}
	}
}
