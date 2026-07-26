// Package main provides the entry point for the audit CLI tool.
// The audit tool scans the Private Buddy codebase for violations of
// the project constitution's five core principles.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"qingqiu-world-ci/audit/internal/audit"
	"qingqiu-world-ci/audit/internal/audit/baseline"
	"qingqiu-world-ci/audit/internal/audit/checker"
	"qingqiu-world-ci/audit/internal/audit/reporter"
)

// Version is set at build time via ldflags.
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "scan":
		runScan(os.Args[2:])
	case "diff":
		runDiff(os.Args[2:])
	case "baseline":
		runBaseline(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("audit version %s\n", Version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: audit <command> [flags]

Commands:
  scan      Run a full audit scan on the codebase
  diff      Compare current scan against saved baseline
  baseline  Manage baseline (save)

Flags for scan:
  --json           Output results as JSON instead of terminal table
  --module <path>  Scan only the specified module path

Flags for diff:
  --json           Output results as JSON

Run 'audit <command>' for more details.
`)
}

// buildCheckers returns the default set of checkers for a scan.
func buildCheckers() []checker.Checker {
	return []checker.Checker{
		checker.NewCommentChecker(),
		checker.NewSilentErrorChecker(),
		checker.NewNullableChecker(),
		checker.NewForeignKeyChecker(),
		checker.NewStringEnumChecker(),
		checker.NewDuplicateChecker(),
	}
}

// generateFingerprints creates fingerprints for all findings in the report.
func generateFingerprints(findings []checker.Finding) {
	for i := range findings {
		findings[i].Fingerprint = baseline.GenerateFingerprintSimple(findings[i])
	}
}

// runScan executes a full audit scan on the project root.
func runScan(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	useJSON := fs.Bool("json", false, "output results as JSON")
	modulePath := fs.String("module", "", "scan only the specified module path")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "scan: %v\n", err)
		os.Exit(2)
	}

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan: cannot determine working directory: %v\n", err)
		os.Exit(2)
	}

	engine := audit.New(buildCheckers(), Version)
	result, err := engine.Run(root, *modulePath, func(current, total int) {
		fmt.Fprintf(os.Stderr, "\rScanning... %d/%d files", current, total)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nscan: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprint(os.Stderr, "\r\033[K")

	// Generate fingerprints for all findings
	generateFingerprints(result.Report.Findings)

	var rep reporter.Reporter
	if *useJSON {
		rep = reporter.NewJSONReporter()
	} else {
		rep = reporter.NewTerminalReporter()
	}

	if err := rep.Report(&result.Report); err != nil {
		fmt.Fprintf(os.Stderr, "scan: report failed: %v\n", err)
		os.Exit(2)
	}

	if result.Report.FindingCount > 0 && !*useJSON {
		fmt.Fprintf(os.Stderr, "\nElapsed: %s\n", result.ElapsedTime.Truncate(0).String())
	}
}

// runBaseline manages the audit baseline (save subcommand).
func runBaseline(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "baseline: expected subcommand (save)")
		os.Exit(1)
	}

	switch args[0] {
	case "save":
		runBaselineSave()
	default:
		fmt.Fprintf(os.Stderr, "baseline: unknown subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

// runBaselineSave runs a full scan and saves the results as the baseline.
func runBaselineSave() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline save: cannot determine working directory: %v\n", err)
		os.Exit(2)
	}

	engine := audit.New(buildCheckers(), Version)
	result, err := engine.Run(root, "", func(current, total int) {
		fmt.Fprintf(os.Stderr, "\rScanning... %d/%d files", current, total)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nbaseline save: scan failed: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprint(os.Stderr, "\r\033[K")

	// Generate fingerprints
	generateFingerprints(result.Report.Findings)

	// Save baseline
	now := time.Now().UTC().Format(time.RFC3339)
	result.Report.CreatedAt = now

	baselineFindings := result.Report.Findings
	if err := baseline.Save(root, baselineFindings, Version); err != nil {
		fmt.Fprintf(os.Stderr, "baseline save: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("Baseline saved: %d findings at %s\n", len(baselineFindings), now)
}

// runDiff compares the current scan against the saved baseline.
func runDiff(args []string) {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	useJSON := fs.Bool("json", false, "output results as JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "diff: %v\n", err)
		os.Exit(2)
	}

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "diff: cannot determine working directory: %v\n", err)
		os.Exit(2)
	}

	// Run current scan
	engine := audit.New(buildCheckers(), Version)
	result, err := engine.Run(root, "", func(current, total int) {
		fmt.Fprintf(os.Stderr, "\rScanning... %d/%d files", current, total)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\ndiff: scan failed: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprint(os.Stderr, "\r\033[K")

	// Generate fingerprints
	generateFingerprints(result.Report.Findings)

	// Compare against baseline
	diffResult, err := baseline.Diff(root, result.Report.Findings)
	if err != nil {
		fmt.Fprintf(os.Stderr, "diff: %v\n", err)
		os.Exit(2)
	}

	if *useJSON {
		printDiffJSON(diffResult)
	} else {
		printDiffResult(diffResult)
	}

	// Exit code: 0 if clean, 1 if new violations
	if len(diffResult.New) > 0 {
		os.Exit(1)
	}
}

// diffOutput is the JSON structure for diff results.
type diffOutput struct {
	NewCount        int               `json:"new_count"`
	ResolvedCount   int               `json:"resolved_count"`
	UnchangedCount  int               `json:"unchanged_count"`
	BaselineCreated string            `json:"baseline_created"`
	BaselineCount   int               `json:"baseline_count"`
	NewFindings     []checker.Finding `json:"new_findings"`
	Resolved        []string          `json:"resolved"`
}

// printDiffJSON outputs the diff result as JSON.
func printDiffJSON(diff *baseline.DiffResult) {
	out := diffOutput{
		NewCount:        len(diff.New),
		ResolvedCount:   len(diff.Resolved),
		UnchangedCount:  diff.Unchanged,
		BaselineCreated: diff.BaselineCreated,
		BaselineCount:   diff.BaselineCount,
		NewFindings:     diff.New,
		Resolved:        diff.Resolved,
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(data))
}

// printDiffResult outputs the diff result in human-readable format.
func printDiffResult(diff *baseline.DiffResult) {
	fmt.Println("=== Diff vs Baseline ===")
	fmt.Printf("Baseline: %s (%d findings)\n\n", diff.BaselineCreated, diff.BaselineCount)
	fmt.Printf("New violations: %d\n", len(diff.New))
	fmt.Printf("Resolved:       %d\n", len(diff.Resolved))
	fmt.Printf("Unchanged:      %d\n", diff.Unchanged)

	if len(diff.New) > 0 {
		fmt.Println("\nNew:")
		for _, f := range diff.New {
			fmt.Printf("  %s:%d  %-15s  [%s]  %s\n",
				f.File, f.Line, f.Check.String(), f.Severity.String(), f.Message)
		}
	}

	if len(diff.New) == 0 {
		fmt.Println("\n✓ No new violations found.")
	} else {
		fmt.Println("\n✗ Fix new violations before committing.")
	}
}
