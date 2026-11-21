// Package testrun applies unreleased migrations to disposable test data.
// Progress belongs to the test run, outside the product data directory. A failed
// preparation ends the run; this package does not recover or rebuild its data.
package testrun

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"qingqiu-world-server/internal/config"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/migration/dev/unreleased"

	"gorm.io/gorm"
)

// Runner retains the migration progress for one test run and binary.
type Runner struct {
	path  string
	state progress
}

type progress struct {
	BaseRelease string `json:"base_release"`
	Revision    int    `json:"revision"`
}

// Open checks test-owned progress before any product data is initialized.
// The caller supplies an absolute path outside dataRoot and owns its lifetime.
func Open(statePath, dataRoot string) (*Runner, error) {
	if !filepath.IsAbs(statePath) {
		return nil, fmt.Errorf("TEST_MIGRATION_STATE_PATH must be an absolute path outside DATA_ROOT")
	}
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve test DATA_ROOT: %w", err)
	}
	rel, err := filepath.Rel(root, statePath)
	if err != nil {
		return nil, fmt.Errorf("resolve test migration progress path: %w", err)
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("TEST_MIGRATION_STATE_PATH must be outside DATA_ROOT")
	}
	steps := unreleased.Registered()
	if err := unreleased.Validate(steps); err != nil {
		return nil, err
	}
	runner := &Runner{path: statePath, state: progress{BaseRelease: config.AppVersion}}
	data, err := os.ReadFile(statePath)
	if os.IsNotExist(err) {
		return runner, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read test migration progress: %w", err)
	}
	runner.state = progress{}
	if err := json.Unmarshal(data, &runner.state); err != nil {
		return nil, fmt.Errorf("parse test migration progress: %w", err)
	}
	if runner.state.BaseRelease != config.AppVersion || runner.state.Revision < 0 || runner.state.Revision > len(steps) {
		return nil, fmt.Errorf("test migration progress does not match this build; prepare new test data")
	}
	return runner, nil
}

// Apply runs missing steps after release migration and before business startup.
// It records each success with a plain file write. Any error ends preparation;
// callers must start a new run instead of retrying partially prepared data.
func (r *Runner) Apply(db *gorm.DB, dataRoot string) error {
	if r.state.Revision == 0 {
		if err := r.writeProgress(); err != nil {
			return err
		}
	}
	for _, step := range unreleased.Registered()[r.state.Revision:] {
		applogger.Info("Executing test migration", "revision", step.Revision, "description", step.Description)
		if err := unreleased.Apply(db, step.Revision, dataRoot); err != nil {
			return fmt.Errorf("test migration %d: %w", step.Revision, err)
		}
		r.state.Revision = step.Revision
		if err := r.writeProgress(); err != nil {
			return err
		}
	}
	applogger.Info("Test migrations completed", "base_release", r.state.BaseRelease, "revision", r.state.Revision)
	return nil
}

func (r *Runner) writeProgress() error {
	data, err := json.Marshal(r.state)
	if err != nil {
		return fmt.Errorf("encode test migration progress: %w", err)
	}
	if err := os.WriteFile(r.path, data, 0644); err != nil {
		return fmt.Errorf("write test migration progress at revision %d: %w", r.state.Revision, err)
	}
	return nil
}
