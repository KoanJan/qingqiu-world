// Package unreleased declares and executes committed development data migrations.
package unreleased

import (
	"embed"
	"fmt"
	"path"
	"strings"

	"qingqiu-world-server/internal/config"

	"gorm.io/gorm"
)

// Step identifies an unreleased data change. Its introducing Git commit is
// resolved from the step-owned source files after the change is committed.
type Step struct {
	BaseRelease  string
	FromRevision int
	Revision     int
	Description  string
	// SQLPath is relative to sourceRoot and is empty when the step ships no SQL.
	SQLPath string
	// Convert performs data work that SQL cannot express. It runs inside the
	// same transaction after the SQL and may be nil.
	Convert func(*gorm.DB, string) error
}

const sourceRoot = "server/internal/migration/dev/unreleased"

// SourceFiles returns the step-owned files whose introducing commit and
// contents must agree. The registry may change to append later steps; these
// files may not. The registry stays the single authority over order and
// execution: scripts/<revision>.sql and scripts/<revision>.go are named after
// their revision so a revision is recognizable at a glance, and their names
// never decide order or what runs. Files that no step registers are never
// executed or tracked.
func (step Step) SourceFiles() []string {
	files := make([]string, 0, 2)
	if step.SQLPath != "" {
		files = append(files, path.Join(sourceRoot, step.SQLPath))
	}
	if step.Convert != nil {
		files = append(files, path.Join(sourceRoot, "scripts", fmt.Sprintf("%d.go", step.Revision)))
	}
	return files
}

// The all: prefix includes dot files, so scripts/.gitkeep keeps the directory
// embeddable once every development script has been folded into a release.
//
//go:embed all:scripts
var sources embed.FS

// Registered returns the ordered registry without exposing its backing slice.
func Registered() []Step {
	return append([]Step(nil), registered...)
}

// Validate rejects gaps, duplicate revisions, or steps without a payload.
// A step must ship SQL, a conversion, or both. When SQLPath is declared the
// file must be embedded: a registered file that is missing stops the flow.
// Go conversions need no file check because the compiler resolves them.
func Validate(list []Step) error {
	for i, step := range list {
		if step.BaseRelease != config.AppVersion {
			return fmt.Errorf("development migration %d is based on release %q, want %q", step.Revision, step.BaseRelease, config.AppVersion)
		}
		if step.FromRevision != i {
			return fmt.Errorf("development migration %d starts at revision %d, want %d", step.Revision, step.FromRevision, i)
		}
		if step.Revision != i+1 {
			return fmt.Errorf("development migration at index %d has revision %d, want %d", i, step.Revision, i+1)
		}
		if step.SQLPath == "" && step.Convert == nil {
			return fmt.Errorf("development migration %d has neither SQL nor a conversion", step.Revision)
		}
		if step.SQLPath == "" {
			continue
		}
		expected := fmt.Sprintf("scripts/%d.sql", step.Revision)
		if step.SQLPath != expected {
			return fmt.Errorf("development migration %d SQL path is %q, want %q", step.Revision, step.SQLPath, expected)
		}
		if _, err := sources.ReadFile(step.SQLPath); err != nil {
			return fmt.Errorf("development migration %d SQL is missing: %w", step.Revision, err)
		}
	}
	return nil
}

// Apply executes one development step on a staged data set. SQL and the Go
// conversion are both optional, but a step must carry at least one; SQL runs
// first and the conversion receives the staged root inside the same
// transaction. A failed step leaves only staging pending, so retry starts from
// a complete active copy or checkpoint instead of skipping applied statements.
func Apply(db *gorm.DB, revision int, dataRoot string) error {
	if err := Validate(registered); err != nil {
		return err
	}
	if revision < 1 || revision > len(registered) {
		return fmt.Errorf("unknown development revision %d", revision)
	}
	if dataRoot == "" {
		return fmt.Errorf("development revision %d has no staged data root", revision)
	}
	step := registered[revision-1]
	content := ""
	if step.SQLPath != "" {
		script, err := sources.ReadFile(step.SQLPath)
		if err != nil {
			return fmt.Errorf("read development SQL %d: %w", revision, err)
		}
		content = strings.TrimSpace(string(script))
		if content == "" || !strings.HasSuffix(content, ";") {
			return fmt.Errorf("development SQL %d must be non-empty and end with a semicolon", revision)
		}
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if content != "" {
			if err := tx.Exec(content).Error; err != nil {
				return fmt.Errorf("execute development SQL %d: %w", revision, err)
			}
		}
		if step.Convert != nil {
			if err := step.Convert(tx, dataRoot); err != nil {
				return fmt.Errorf("convert development revision %d: %w", revision, err)
			}
		}
		return nil
	})
}
