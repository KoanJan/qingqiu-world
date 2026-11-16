package dev

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"qingqiu-world-server/internal/migration/dev/unreleased"
)

const stateFile = ".dev-data-version.json"
const stateFormat = 1

// Persisted kind and status identifiers belong to the state file and manifest
// format. Changing any value requires a new stateFormat because existing
// baselines and checkpoints are validated against them.
const (
	kindBaseline   = "baseline"
	kindCheckpoint = "checkpoint"
	statusComplete = "complete"
	statusPending  = "pending"
)

// Quarantine reasons label a retained active copy by the transition that
// replaced it, keeping manual diagnosis readable.
const (
	reasonSeed    = "seed"
	reasonUpgrade = "upgrade"
	reasonPromote = "promote"
	reasonFailed  = "failed"
)

// AppliedStep records the Git commit that introduced one development revision.
type AppliedStep struct {
	Revision int    `json:"revision"`
	Commit   string `json:"commit"`
}

// State belongs to an active data copy or a checkpoint, never a release baseline.
type State struct {
	FormatVersion     int           `json:"format_version"`
	BaseRelease       string        `json:"base_release"`
	BaselineID        string        `json:"baseline_id"`
	BaseCommit        string        `json:"base_commit"`
	DevRevision       int           `json:"dev_revision"`
	DevCommit         string        `json:"dev_commit,omitempty"`
	AppliedMigrations []AppliedStep `json:"applied_migrations"`
	PendingRevision   int           `json:"pending_revision,omitempty"`
	Status            string        `json:"status"`
}

// Manifest validates an immutable released baseline or development checkpoint.
type Manifest struct {
	FormatVersion  int           `json:"format_version"`
	Kind           string        `json:"kind"`
	Release        string        `json:"release"`
	ReleaseTag     string        `json:"release_tag"`
	ReleaseCommit  string        `json:"release_commit"`
	SourceBaseline string        `json:"source_baseline,omitempty"`
	Database       string        `json:"database_version"`
	DataRoots      []string      `json:"data_roots"`
	Chain          []AppliedStep `json:"chain,omitempty"`
	Digest         string        `json:"digest"`
	ID             string        `json:"id"`
	Status         string        `json:"status"`
}

func initialState(manifest Manifest) State {
	return State{FormatVersion: stateFormat, BaseRelease: manifest.Release,
		BaselineID: manifest.ID, BaseCommit: manifest.ReleaseCommit,
		AppliedMigrations: []AppliedStep{}, Status: statusComplete}
}

func (s State) validate(base Manifest) error {
	if s.FormatVersion != stateFormat || s.Status != statusComplete ||
		s.BaseRelease != base.Release || s.BaselineID != base.ID || s.BaseCommit != base.ReleaseCommit {
		return fmt.Errorf("development state does not match the completed release baseline")
	}
	if s.DevRevision != len(s.AppliedMigrations) {
		return fmt.Errorf("development revision %d does not match chain length %d", s.DevRevision, len(s.AppliedMigrations))
	}
	if s.PendingRevision != 0 {
		return fmt.Errorf("completed development state still has pending revision %d", s.PendingRevision)
	}
	for i, step := range s.AppliedMigrations {
		if step.Revision != i+1 || len(step.Commit) != 40 {
			return fmt.Errorf("invalid development chain entry at revision %d", i+1)
		}
	}
	if s.DevRevision == 0 && s.DevCommit != "" {
		return fmt.Errorf("dev.0 must not have a development commit")
	}
	if s.DevRevision > 0 && s.DevCommit != s.AppliedMigrations[len(s.AppliedMigrations)-1].Commit {
		return fmt.Errorf("development commit does not match chain tip")
	}
	return nil
}

func commonPrefix(a, b []AppliedStep) int {
	length := len(a)
	if len(b) < length {
		length = len(b)
	}
	i := 0
	for i < length && a[i] == b[i] {
		i++
	}
	return i
}

func stateAt(base Manifest, chain []AppliedStep) State {
	state := initialState(base)
	state.AppliedMigrations = append([]AppliedStep{}, chain...)
	state.DevRevision = len(chain)
	if len(chain) > 0 {
		state.DevCommit = chain[len(chain)-1].Commit
	}
	return state
}

func checkpointName(baseID string, step AppliedStep) string {
	return filepath.Join(baseID, "dev."+strconv.Itoa(step.Revision)+"."+step.Commit)
}

func manifestID(manifest Manifest) string {
	manifest.ID = ""
	content, _ := json.Marshal(manifest)
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func validateManifest(path string, expectedKind string) (Manifest, error) {
	var manifest Manifest
	if err := readJSON(filepath.Join(path, "manifest.json"), &manifest); err != nil {
		return manifest, err
	}
	if manifest.FormatVersion != stateFormat || manifest.Kind != expectedKind || manifest.Status != statusComplete ||
		manifest.Release == "" || manifest.ReleaseCommit == "" || manifest.Digest == "" || manifest.ID == "" {
		return manifest, fmt.Errorf("invalid %s manifest at %s", expectedKind, path)
	}
	if manifest.ID != manifestID(manifest) {
		return manifest, fmt.Errorf("%s manifest ID mismatch at %s", expectedKind, path)
	}
	digest, err := treeDigest(filepath.Join(path, "data"))
	if err != nil {
		return manifest, err
	}
	if digest != manifest.Digest {
		return manifest, fmt.Errorf("%s data digest mismatch at %s", expectedKind, path)
	}
	return manifest, nil
}

func createManifest(path string, manifest Manifest) (Manifest, error) {
	manifest.FormatVersion = stateFormat
	manifest.Status = statusComplete
	if err := normalizeSQLiteWAL(filepath.Join(path, "data")); err != nil {
		return manifest, err
	}
	digest, err := treeDigest(filepath.Join(path, "data"))
	if err != nil {
		return manifest, err
	}
	manifest.Digest = digest
	manifest.ID = manifestID(manifest)
	if err := writeJSONAtomic(filepath.Join(path, "manifest.json"), manifest); err != nil {
		return manifest, err
	}
	return validateManifest(path, manifest.Kind)
}

func loadState(root string) (State, error) {
	var state State
	if err := readJSON(filepath.Join(root, stateFile), &state); err != nil {
		if os.IsNotExist(err) {
			return state, fmt.Errorf("development state missing from active data")
		}
		return state, err
	}
	return state, nil
}

func targetChain(list []unreleased.Step, commitFor func(string) (string, error)) ([]AppliedStep, error) {
	if err := unreleased.Validate(list); err != nil {
		return nil, err
	}
	chain := make([]AppliedStep, 0, len(list))
	for _, step := range list {
		var commit string
		for _, source := range step.SourceFiles() {
			introduced, err := commitFor(source)
			if err != nil {
				return nil, err
			}
			if commit != "" && commit != introduced {
				return nil, fmt.Errorf("development revision %d has files introduced in different commits", step.Revision)
			}
			commit = introduced
		}
		chain = append(chain, AppliedStep{Revision: step.Revision, Commit: commit})
	}
	return chain, nil
}
