// Package audit provides the core audit engine that orchestrates file scanning
// and checker execution across the codebase.
package audit

import (
	"fmt"
	"os"
	"time"

	"qingqiu-world-ci/audit/internal/audit/checker"
	"qingqiu-world-ci/audit/internal/audit/reporter"
	"qingqiu-world-ci/audit/internal/audit/scanner"
)

// Engine orchestrates the audit scan pipeline: file discovery → checker dispatch → report generation.
type Engine struct {
	checkers []checker.Checker
	version  string
}

// New creates a new audit Engine with the given checkers.
func New(checkers []checker.Checker, version string) *Engine {
	return &Engine{
		checkers: checkers,
		version:  version,
	}
}

// ScanResult holds the complete result of an audit scan.
type ScanResult struct {
	Report      reporter.Report
	ElapsedTime time.Duration
}

// Run executes a full audit scan on the project at root.
// If modulePath is non-empty, only files under that path are scanned.
// progressFn is called periodically with scan progress information.
func (e *Engine) Run(root string, modulePath string, progressFn func(current, total int)) (*ScanResult, error) {
	// Discover files
	files, err := scanner.Scan(root, modulePath)
	if err != nil {
		return nil, fmt.Errorf("scan discovery failed: %w", err)
	}

	start := time.Now()
	var allFindings []checker.Finding

	// For each file, run applicable checkers
	for i, f := range files {
		if progressFn != nil {
			progressFn(i+1, len(files))
		}

		ext := fileExt(f.Path)

		for _, c := range e.checkers {
			if !c.Accept(ext) {
				continue
			}

			findings, err := c.Check(f.Path, f.Content)
			if err != nil {
				fmt.Fprintf(os.Stderr, "checker %s failed on %s: %v\n", c.Name(), f.Path, err)
				continue
			}
			allFindings = append(allFindings, findings...)
		}
	}

	elapsed := time.Since(start)

	report := reporter.Report{
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		Version:      e.version,
		TotalFiles:   len(files),
		FindingCount: len(allFindings),
		Findings:     allFindings,
		Summary:      reporter.ComputeSummary(allFindings),
	}

	return &ScanResult{
		Report:      report,
		ElapsedTime: elapsed,
	}, nil
}

// fileExt returns the file extension (including the dot) in lowercase.
func fileExt(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i:]
		}
	}
	return ""
}
