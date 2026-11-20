// Package dev manages released baselines and committed development data.
// It runs before the active database connection or business services start.
package dev

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/migration/dev/unreleased"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/gofrs/flock"
)

const modeEnv = "DEV_DATA_MODE"

type manager struct {
	active string
	store  string
	repo   *Repository
	lock   *flock.Flock
}

type bootstrapRecord struct {
	BaselineID string `json:"baseline_id"`
	Release    string `json:"release"`
}

func (m *manager) bootstrapJournal() string { return filepath.Join(m.store, "bootstrap-pending.json") }

// Start reconciles developer data before database.Init. The returned function
// holds the data-set lock for the full server lifetime, preventing two writers.
func Start(buildCommit string) (func(), error) {
	if os.Getenv(modeEnv) != "1" {
		return func() {}, nil
	}
	m, err := newManager()
	if err != nil {
		return nil, err
	}
	// Verify the checkout before creating the managed store: a rejected start
	// must not leave .developer-data, its lock, or any managed directory behind.
	releaseCommit, target, err := m.verifySource(buildCommit)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(m.store, 0700); err != nil {
		return nil, fmt.Errorf("create developer data store: %w", err)
	}
	m.lock = flock.New(filepath.Join(m.store, ".lock"))
	locked, err := m.lock.TryLock()
	if err != nil || !locked {
		return nil, fmt.Errorf("developer data is already in use: %v", err)
	}
	unlock := func() { _ = m.lock.Unlock() }
	if err := m.run(releaseCommit, target); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

func newManager() (*manager, error) {
	settings := config.Get()
	active, err := absoluteRoot(settings.DataRoot)
	if err != nil {
		return nil, err
	}
	m := &manager{active: active, store: active + ".developer-data"}
	if inside(active, m.store) {
		return nil, fmt.Errorf("developer data store must be outside DATA_ROOT")
	}
	if info, err := os.Lstat(m.store); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("developer data store must not be a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if info, err := os.Lstat(active); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("DATA_ROOT must not be a symlink in automatic developer mode")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return m, nil
}

// verifySource proves the running binary corresponds to a committed checkout
// that may advance. Every check is read-only, so it runs before the managed
// store exists: a rejected checkout cannot create or modify one.
func (m *manager) verifySource(buildCommit string) (*object.Commit, []AppliedStep, error) {
	repoRoot := os.Getenv("DEV_REPO_ROOT")
	if repoRoot == "" {
		return nil, nil, fmt.Errorf("DEV_REPO_ROOT must identify the developer checkout")
	}
	repo, err := Open(repoRoot)
	if err != nil {
		return nil, nil, err
	}
	m.repo = repo
	releaseCommit, err := repo.ReleaseCommit(config.AppVersion)
	if err != nil {
		return nil, nil, err
	}
	if err := repo.Clean(); err != nil {
		return nil, nil, err
	}
	head, err := repo.Head()
	if err != nil {
		return nil, nil, err
	}
	if buildCommit == "" || buildCommit != head.Hash.String() {
		return nil, nil, fmt.Errorf("running server binary does not match checked-out commit %s", head.Hash)
	}
	if _, err := repo.FirstParentPath(releaseCommit, head); err != nil {
		return nil, nil, err
	}
	target, err := targetChain(unreleased.Registered(), func(path string) (string, error) {
		return repo.IntroducingCommit(path, releaseCommit, head)
	})
	if err != nil {
		return nil, nil, err
	}
	stepCommits := make(map[string]bool, len(target))
	for _, step := range target {
		stepCommits[step.Commit] = true
	}
	if err := repo.ValidateRegisteredChanges(releaseCommit, head, stepCommits); err != nil {
		return nil, nil, err
	}
	return releaseCommit, target, nil
}

func (m *manager) run(releaseCommit *object.Commit, target []AppliedStep) error {
	if err := m.recoverSwitch(); err != nil {
		return err
	}
	base, found, err := m.latestBaseline()
	if err != nil {
		return err
	}
	if !found {
		base, err = m.bootstrapBaseline(releaseCommit)
		if err != nil {
			return err
		}
	}
	if compareVersions(base.Release, config.AppVersion) > 0 {
		return fmt.Errorf("developer baseline %s is newer than released application %s", base.Release, config.AppVersion)
	}
	if compareVersions(base.Release, config.AppVersion) < 0 {
		base, err = m.promoteBaseline(base, releaseCommit)
		if err != nil {
			return err
		}
	}
	if base.ReleaseCommit != releaseCommit.Hash.String() {
		return fmt.Errorf("baseline %s release commit does not match trusted tag", base.Release)
	}
	if err := m.prepareActive(base); err != nil {
		return err
	}
	return m.reconcile(base, target)
}

func compareVersions(a, b string) int {
	parse := func(version string) [3]int {
		var parts [3]int
		for i, component := range strings.Split(version, ".") {
			if i >= len(parts) {
				break
			}
			parts[i], _ = strconv.Atoi(component)
		}
		return parts
	}
	x, y := parse(a), parse(b)
	for i := range x {
		if x[i] < y[i] {
			return -1
		}
		if x[i] > y[i] {
			return 1
		}
	}
	return 0
}

func (m *manager) latestBaseline() (Manifest, bool, error) {
	root := filepath.Join(m.store, "baseline")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return Manifest{}, false, nil
	}
	if err != nil {
		return Manifest{}, false, err
	}
	var latest Manifest
	for _, entry := range entries {
		if !entry.IsDir() {
			return Manifest{}, false, fmt.Errorf("unexpected file in baseline store: %s", entry.Name())
		}
		manifest, err := validateManifest(filepath.Join(root, entry.Name()), kindBaseline)
		if err != nil {
			return Manifest{}, false, err
		}
		if manifest.Release != entry.Name() || manifest.Database != manifest.Release {
			return Manifest{}, false, fmt.Errorf("baseline version mismatch at %s", entry.Name())
		}
		if len(manifest.DataRoots) != 1 || manifest.DataRoots[0] != m.active {
			return Manifest{}, false, fmt.Errorf("baseline data roots do not match configured DATA_ROOT")
		}
		tag, err := m.repo.ReleaseCommit(manifest.Release)
		if err != nil || tag.Hash.String() != manifest.ReleaseCommit {
			return Manifest{}, false, fmt.Errorf("baseline %s does not match its trusted release tag: %v", manifest.Release, err)
		}
		if latest.Release == "" || compareVersions(latest.Release, manifest.Release) < 0 {
			latest = manifest
		}
	}
	return latest, latest.Release != "", nil
}

func (m *manager) bootstrapBaseline(currentRelease *object.Commit) (Manifest, error) {
	stage, err := m.newStage("baseline-")
	if err != nil {
		return Manifest{}, err
	}
	data := filepath.Join(stage, "data")
	backup := os.Getenv("DEV_BASELINE_SOURCE")
	version := config.AppVersion
	release := currentRelease
	if backup != "" {
		backup, err = absoluteRoot(backup)
		if err != nil {
			return Manifest{}, err
		}
		if inside(m.active, backup) || inside(backup, m.active) || inside(m.store, backup) || inside(backup, m.store) {
			return Manifest{}, fmt.Errorf("baseline source must be outside active and managed data")
		}
		if err := copyTree(backup, data); err != nil {
			return Manifest{}, fmt.Errorf("copy supplied release baseline: %w", err)
		}
		version, err = inspectDataDB(data)
		if err != nil {
			return Manifest{}, fmt.Errorf("inspect supplied release baseline: %w", err)
		}
		if compareVersions(version, config.AppVersion) > 0 {
			return Manifest{}, fmt.Errorf("supplied baseline %s is newer than released application %s", version, config.AppVersion)
		}
		release, err = m.repo.ReleaseCommit(version)
		if err != nil {
			return Manifest{}, err
		}
	} else {
		empty, err := emptyActive(m.active)
		if err != nil {
			return Manifest{}, err
		}
		if !empty {
			return Manifest{}, fmt.Errorf("existing developer data has no trusted release baseline; provide DEV_BASELINE_SOURCE")
		}
		if version == "0.1.21" {
			// v0.1.21 predates isolated release execution; its released
			// LLM table is frozen in the initializer used here.
			db, err := openDataDB(data)
			if err != nil {
				return Manifest{}, err
			}
			initErr := database.InitializeFreshAt(db, version)
			closeErr := closeDataDB(db)
			if initErr != nil {
				return Manifest{}, fmt.Errorf("initialize released baseline: %w", initErr)
			}
			if closeErr != nil {
				return Manifest{}, closeErr
			}
		} else {
			if err := m.runTaggedRelease(data, release); err != nil {
				return Manifest{}, fmt.Errorf("initialize baseline with release %s: %w", version, err)
			}
		}
	}
	verifiedVersion, err := inspectDataDB(data)
	if err != nil || verifiedVersion != version {
		return Manifest{}, fmt.Errorf("baseline database version %q, want %s: %v", verifiedVersion, version, err)
	}
	if _, err := os.Stat(filepath.Join(data, stateFile)); err == nil {
		return Manifest{}, fmt.Errorf("released baseline contains development state")
	} else if !os.IsNotExist(err) {
		return Manifest{}, err
	}
	if err := normalizeSQLiteWAL(data); err != nil {
		return Manifest{}, err
	}
	manifest, err := createManifest(stage, Manifest{Kind: kindBaseline, Release: version,
		ReleaseTag: "v" + version, ReleaseCommit: release.Hash.String(),
		Database: version, DataRoots: []string{m.active}})
	if err != nil {
		return Manifest{}, err
	}
	if err := os.MkdirAll(filepath.Join(m.store, "baseline"), 0700); err != nil {
		return Manifest{}, err
	}
	if err := writeJSONAtomic(m.bootstrapJournal(), bootstrapRecord{BaselineID: manifest.ID, Release: version}); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(stage, filepath.Join(m.store, "baseline", version)); err != nil {
		return Manifest{}, err
	}
	if err := m.seedActive(manifest); err != nil {
		return Manifest{}, err
	}
	if err := os.Remove(m.bootstrapJournal()); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (m *manager) newStage(prefix string) (string, error) {
	root := filepath.Join(m.store, "staging")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, prefix)
}

func emptyActive(root string) (bool, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case ".DS_Store", ".localized", "Thumbs.db", "desktop.ini":
			continue
		}
		return false, nil
	}
	return true, nil
}

func (m *manager) ensureActive(base Manifest) error {
	_, err := os.Stat(m.active)
	if err == nil {
		empty, err := emptyActive(m.active)
		if err != nil {
			return err
		}
		if !empty {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return m.seedActive(base)
}

func (m *manager) seedActive(base Manifest) error {
	stage, err := m.newStage("active-")
	if err != nil {
		return err
	}
	if err := copyTree(filepath.Join(m.store, "baseline", base.Release, "data"), filepath.Join(stage, "data")); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(stage, "data", stateFile), initialState(base)); err != nil {
		return err
	}
	return m.switchActive(filepath.Join(stage, "data"), reasonSeed)
}

// prepareActive completes a release-boundary switch left unfinished by a
// crash. It never treats an old development active copy as a new baseline.
func (m *manager) prepareActive(base Manifest) error {
	empty, err := emptyActive(m.active)
	if err != nil {
		return err
	}
	if empty {
		if err := m.ensureActive(base); err != nil {
			return err
		}
		if _, err := os.Stat(m.bootstrapJournal()); err == nil {
			return os.Remove(m.bootstrapJournal())
		}
		return nil
	}
	state, err := loadState(m.active)
	if err != nil {
		var pending bootstrapRecord
		if readJSON(m.bootstrapJournal(), &pending) == nil && pending.BaselineID == base.ID && pending.Release == base.Release {
			if err := m.seedActive(base); err != nil {
				return err
			}
			return os.Remove(m.bootstrapJournal())
		}
		return err
	}
	if state.BaseRelease == base.Release && state.BaselineID == base.ID {
		if _, err := os.Stat(m.bootstrapJournal()); err == nil {
			return os.Remove(m.bootstrapJournal())
		}
		return nil
	}
	if state.Status != statusComplete || compareVersions(state.BaseRelease, base.Release) >= 0 {
		return fmt.Errorf("active developer state does not match trusted baseline; preserve it for manual recovery")
	}
	return m.seedActive(base)
}

func (m *manager) reconcile(base Manifest, target []AppliedStep) error {
	state, err := loadState(m.active)
	if err != nil {
		return err
	}
	if err := state.validate(base); err != nil {
		return err
	}
	version, err := inspectDataDB(m.active)
	if err != nil || version != base.Release {
		return fmt.Errorf("active database release %q differs from baseline %s: %v", version, base.Release, err)
	}
	prefix := commonPrefix(state.AppliedMigrations, target)
	if prefix == len(state.AppliedMigrations) && prefix == len(target) {
		return nil
	}
	source, from, err := m.rebuildSource(base, state, target, prefix)
	if err != nil {
		return err
	}
	stage, err := m.newStage("dev-")
	if err != nil {
		return err
	}
	data := filepath.Join(stage, "data")
	if err := copyTree(source, data); err != nil {
		return err
	}
	for i := from; i < len(target); i++ {
		pending := stateAt(base, target[:i])
		pending.Status = statusPending
		pending.PendingRevision = target[i].Revision
		if err := writeJSONAtomic(filepath.Join(data, stateFile), pending); err != nil {
			return err
		}
		db, err := openDataDB(data)
		if err != nil {
			return err
		}
		stepErr := unreleased.Apply(db, target[i].Revision, data)
		closeErr := closeDataDB(db)
		if stepErr != nil {
			return stepErr
		}
		if closeErr != nil {
			return closeErr
		}
		complete := stateAt(base, target[:i+1])
		if err := writeJSONAtomic(filepath.Join(data, stateFile), complete); err != nil {
			return err
		}
		if err := m.saveCheckpoint(base, complete, data); err != nil {
			return err
		}
	}
	if from == len(target) {
		if err := writeJSONAtomic(filepath.Join(data, stateFile), stateAt(base, target)); err != nil {
			return err
		}
	}
	version, err = inspectDataDB(data)
	if err != nil || version != base.Release {
		return fmt.Errorf("staged developer database release %q differs from %s: %v", version, base.Release, err)
	}
	return m.switchActive(data, reasonUpgrade)
}

func (m *manager) rebuildSource(base Manifest, state State, target []AppliedStep, prefix int) (string, int, error) {
	if prefix == len(state.AppliedMigrations) {
		return m.active, prefix, nil
	}
	for i := prefix; i > 0; i-- {
		path := filepath.Join(m.store, "checkpoint", checkpointName(base.ID, target[i-1]))
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return "", 0, err
		}
		manifest, err := validateManifest(path, kindCheckpoint)
		if err != nil {
			return "", 0, err
		}
		if manifest.SourceBaseline != base.ID || commonPrefix(manifest.Chain, target[:i]) != i || len(manifest.Chain) != i {
			return "", 0, fmt.Errorf("checkpoint chain mismatch at revision %d", i)
		}
		checkpointState, err := loadState(filepath.Join(path, "data"))
		if err != nil {
			return "", 0, fmt.Errorf("checkpoint state mismatch at revision %d: %v", i, err)
		}
		if err := checkpointState.validate(base); err != nil {
			return "", 0, fmt.Errorf("checkpoint state mismatch at revision %d: %w", i, err)
		}
		if checkpointState.DevRevision != i || commonPrefix(checkpointState.AppliedMigrations, target[:i]) != i {
			return "", 0, fmt.Errorf("checkpoint chain mismatch at revision %d", i)
		}
		version, err := inspectImmutableDataDB(filepath.Join(path, "data"))
		if err != nil || version != base.Release {
			return "", 0, fmt.Errorf("checkpoint database version %q differs from %s: %v", version, base.Release, err)
		}
		return filepath.Join(path, "data"), i, nil
	}
	return filepath.Join(m.store, "baseline", base.Release, "data"), 0, nil
}

func (m *manager) saveCheckpoint(base Manifest, state State, data string) error {
	step := state.AppliedMigrations[len(state.AppliedMigrations)-1]
	destination := filepath.Join(m.store, "checkpoint", checkpointName(base.ID, step))
	if _, err := os.Stat(destination); err == nil {
		manifest, err := validateManifest(destination, kindCheckpoint)
		if err != nil || manifest.SourceBaseline != base.ID || len(manifest.Chain) != len(state.AppliedMigrations) ||
			commonPrefix(manifest.Chain, state.AppliedMigrations) != len(state.AppliedMigrations) {
			return fmt.Errorf("existing checkpoint invalid at %s: %v", destination, err)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	stage, err := m.newStage("checkpoint-")
	if err != nil {
		return err
	}
	if err := copyTree(data, filepath.Join(stage, "data")); err != nil {
		return err
	}
	_, err = createManifest(stage, Manifest{Kind: kindCheckpoint, Release: base.Release,
		ReleaseTag: base.ReleaseTag, ReleaseCommit: base.ReleaseCommit,
		SourceBaseline: base.ID, Database: base.Release, DataRoots: []string{m.active}, Chain: state.AppliedMigrations})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	return os.Rename(stage, destination)
}

// promoteBaseline is implemented with the final release tag's own code, never
// by applying its migration to the previous development active directory.
func (m *manager) promoteBaseline(old Manifest, release *object.Commit) (Manifest, error) {
	oldRelease, err := m.repo.ReleaseCommit(old.Release)
	if err != nil {
		return Manifest{}, err
	}
	if _, err := m.repo.FirstParentPath(oldRelease, release); err != nil {
		return Manifest{}, fmt.Errorf("release tag v%s does not descend from baseline release v%s: %w", config.AppVersion, old.Release, err)
	}
	stage, err := m.newStage("promotion-")
	if err != nil {
		return Manifest{}, err
	}
	data := filepath.Join(stage, "data")
	if err := copyTree(filepath.Join(m.store, "baseline", old.Release, "data"), data); err != nil {
		return Manifest{}, err
	}
	if err := m.runTaggedRelease(data, release); err != nil {
		return Manifest{}, err
	}
	version, err := inspectDataDB(data)
	if err != nil || version != config.AppVersion {
		return Manifest{}, fmt.Errorf("promoted database version %q, want %s: %v", version, config.AppVersion, err)
	}
	manifest, err := createManifest(stage, Manifest{Kind: kindBaseline, Release: config.AppVersion,
		ReleaseTag: "v" + config.AppVersion, ReleaseCommit: release.Hash.String(),
		SourceBaseline: old.ID, Database: version, DataRoots: []string{m.active}})
	if err != nil {
		return Manifest{}, err
	}
	newBaseline := filepath.Join(m.store, "baseline", config.AppVersion)
	if err := os.Rename(stage, newBaseline); err != nil {
		return Manifest{}, err
	}
	active, err := m.newStage("new-release-active-")
	if err != nil {
		return Manifest{}, err
	}
	if err := copyTree(filepath.Join(newBaseline, "data"), filepath.Join(active, "data")); err != nil {
		return Manifest{}, err
	}
	if err := writeJSONAtomic(filepath.Join(active, "data", stateFile), initialState(manifest)); err != nil {
		return Manifest{}, err
	}
	if err := m.switchActive(filepath.Join(active, "data"), reasonPromote); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// quarantineLabel summarizes the active data being replaced so retained copies
// can be identified without opening every database. Data without readable
// state was never managed by this lifecycle.
func (m *manager) quarantineLabel(reason string) string {
	state, err := loadState(m.active)
	if err != nil {
		if _, statErr := os.Stat(filepath.Join(m.active, stateFile)); statErr == nil {
			return "invalid-state_" + reason
		}
		return "unmanaged_" + reason
	}
	label := fmt.Sprintf("%s_dev.%d", state.BaseRelease, state.DevRevision)
	if state.DevRevision > 0 && len(state.DevCommit) >= 12 {
		label += "." + state.DevCommit[:12]
	}
	return label + "_" + reason
}

// quarantineName keeps retained copies unique and sorted by creation time.
func quarantineName(label string) string {
	return label + "_" + time.Now().UTC().Format("20060102T150405.000000000")
}
