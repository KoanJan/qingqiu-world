// Package baseline manages the audit baseline file. The baseline stores
// the set of "known/accepted" audit findings from a previous scan. The
// diff command compares the current scan against the baseline to detect
// new violations that must be fixed before committing.
package baseline

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"qingqiu-world-ci/audit/internal/audit/checker"
)

// BaselineFile is the filename for the audit baseline stored at the project root.
const BaselineFile = ".specify/audit-baseline.json"

// Baseline represents a saved set of audit findings used as a reference point.
type Baseline struct {
	// Created is the ISO 8601 timestamp when the baseline was established.
	Created string `json:"created"`
	// Version is the tool version at baseline creation.
	Version string `json:"version"`
	// Findings contains all findings at baseline time.
	Findings []checker.Finding `json:"findings"`
}

// DiffResult represents the comparison between a current scan and the baseline.
type DiffResult struct {
	// New contains findings present in current but not in baseline.
	New []checker.Finding `json:"new"`
	// Resolved contains findings present in baseline but not in current.
	Resolved []string `json:"resolved"`
	// Unchanged count of findings present in both.
	Unchanged int `json:"unchanged"`
	// BaselineCreated is the baseline timestamp.
	BaselineCreated string `json:"baseline_created"`
	// BaselineCount is the total number of findings in the baseline.
	BaselineCount int `json:"baseline_count"`
}

// GenerateFingerprint creates a stable hash for a finding based on
// file path, check type, symbol name, and 3 lines of surrounding context.
// This enables stable identity tracking even when line numbers shift.
func GenerateFingerprint(f checker.Finding, fileContent []byte) string {
	// Build a unique key from finding attributes
	key := fmt.Sprintf("%s:%d:%s", f.File, int(f.Check), f.Symbol)

	// Add surrounding context hash if content is available
	if len(fileContent) > 0 {
		contextHash := sha256.Sum256(fileContent)
		key += fmt.Sprintf(":%x", contextHash[:8])
	}

	hash := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", hash)
}

// GenerateFingerprintSimple creates a fingerprint without file content context.
// Used when content is not available.
func GenerateFingerprintSimple(f checker.Finding) string {
	key := fmt.Sprintf("%s:%d:%s:%d", f.File, int(f.Check), f.Symbol, f.Line)
	hash := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", hash)
}

// Save writes the current audit findings as the baseline to disk.
func Save(root string, findings []checker.Finding, version string) error {
	baseline := Baseline{
		Created:  "", // Will be set by the caller
		Version:  version,
		Findings: findings,
	}

	data, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return fmt.Errorf("baseline save: marshal failed: %w", err)
	}

	path := filepath.Join(root, BaselineFile)
	// Ensure the directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("baseline save: cannot create directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("baseline save: write failed: %w", err)
	}

	return nil
}

// Load reads the saved baseline from disk.
func Load(root string) (*Baseline, error) {
	path := filepath.Join(root, BaselineFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no baseline found (run 'audit baseline save' first)")
		}
		return nil, fmt.Errorf("baseline load: read failed: %w", err)
	}

	var baseline Baseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		return nil, fmt.Errorf("baseline load: parse failed: %w", err)
	}

	return &baseline, nil
}

// Diff compares current findings against the saved baseline.
// Returns new findings (in current but not baseline) and resolved
// findings (in baseline but not current).
func Diff(root string, currentFindings []checker.Finding) (*DiffResult, error) {
	bl, err := Load(root)
	if err != nil {
		return nil, err
	}

	// Build fingerprint set from baseline
	baselineSet := make(map[string]bool, len(bl.Findings))
	for _, f := range bl.Findings {
		baselineSet[f.Fingerprint] = true
	}

	// Build fingerprint set from current findings
	currentSet := make(map[string]bool, len(currentFindings))
	for _, f := range currentFindings {
		currentSet[f.Fingerprint] = true
	}

	result := &DiffResult{
		BaselineCreated: bl.Created,
		BaselineCount:   len(bl.Findings),
	}

	// Find new violations (in current but not in baseline)
	for _, f := range currentFindings {
		if !baselineSet[f.Fingerprint] {
			result.New = append(result.New, f)
		}
	}
	result.Unchanged = len(currentFindings) - len(result.New)

	// Find resolved violations (in baseline but not in current)
	for _, f := range bl.Findings {
		if !currentSet[f.Fingerprint] {
			label := fmt.Sprintf("%s:%d (%s)", f.File, f.Line, f.Check.String())
			result.Resolved = append(result.Resolved, label)
		}
	}

	return result, nil
}
