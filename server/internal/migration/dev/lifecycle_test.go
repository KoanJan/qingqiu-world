package dev

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"qingqiu-world-server/internal/config"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestDeveloperBaselineMigrationAndRebase(t *testing.T) {
	root := t.TempDir()
	repoPath := filepath.Join(root, "source")
	if err := os.MkdirAll(repoPath, 0700); err != nil {
		t.Fatal(err)
	}
	repo, err := git.PlainInit(repoPath, false)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	writeSource := func(content string) {
		t.Helper()
		source := "server/internal/migration/dev/unreleased/scripts/1.sql"
		path := filepath.Join(repoPath, source)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := worktree.Add(source); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(message string) plumbing.Hash {
		t.Helper()
		hash, err := worktree.Commit(message, &git.CommitOptions{Author: &object.Signature{
			Name: "Developer Data Test", Email: "test@example.invalid", When: time.Now(),
		}})
		if err != nil {
			t.Fatal(err)
		}
		return hash
	}
	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("released"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	releaseHash := commit("release")
	if _, err := repo.CreateTag("v0.1.21", releaseHash, nil); err != nil {
		t.Fatal(err)
	}
	writeSource("development step A")
	firstHash := commit("dev.1 A")

	active := filepath.Join(root, "data")
	t.Setenv("DATA_ROOT", active)
	t.Setenv("LOG_DIR", filepath.Join(root, "logs"))
	t.Setenv("DEV_DATA_MODE", "1")
	t.Setenv("DEV_REPO_ROOT", repoPath)
	config.Init()
	unlock, err := Start(firstHash.String())
	if err != nil {
		t.Fatal(err)
	}
	unlock()

	base := filepath.Join(active+".developer-data", "baseline", "0.1.21", "data")
	if _, err := validateManifest(filepath.Dir(base), kindBaseline); err != nil {
		t.Fatalf("new baseline already invalid: %v", err)
	}
	if hasProbeColumnImmutable(t, base) {
		t.Fatal("baseline contains a development-only column")
	}
	if _, err := validateManifest(filepath.Dir(base), kindBaseline); err != nil {
		t.Fatalf("reading baseline changed it: %v", err)
	}
	if version, err := inspectDataDB(active); err != nil || version != "0.1.21" {
		t.Fatalf("active release version = %q, error = %v", version, err)
	}
	if !hasProbeColumn(t, active) {
		t.Fatal("dev.1 was not confined to active data")
	}
	if _, err := validateManifest(filepath.Dir(base), kindBaseline); err != nil {
		t.Fatalf("reading active data changed baseline: %v", err)
	}
	state, err := loadState(active)
	if err != nil || state.DevRevision != 1 || state.DevCommit != firstHash.String() {
		t.Fatalf("active development state = %+v, error = %v", state, err)
	}
	managed := &manager{active: active, store: active + ".developer-data"}
	baseManifest, err := validateManifest(filepath.Dir(base), kindBaseline)
	if err != nil {
		t.Fatal(err)
	}
	oldSecond := AppliedStep{Revision: 2, Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	newSecond := AppliedStep{Revision: 2, Commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	checkpointSource, from, err := managed.rebuildSource(baseManifest,
		stateAt(baseManifest, []AppliedStep{state.AppliedMigrations[0], oldSecond}),
		[]AppliedStep{state.AppliedMigrations[0], newSecond}, 1)
	if err != nil || from != 1 || !inside(filepath.Join(managed.store, "checkpoint"), checkpointSource) {
		t.Fatalf("common-prefix checkpoint = %q, from = %d, error = %v", checkpointSource, from, err)
	}
	interrupted, err := managed.newStage("interrupted-switch-")
	if err != nil {
		t.Fatal(err)
	}
	stagedData := filepath.Join(interrupted, "data")
	if err := copyTree(active, stagedData); err != nil {
		t.Fatal(err)
	}
	quarantine := filepath.Join(managed.store, "quarantine", "interrupted-old")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(managed.switchJournal(), switchRecord{StagedData: stagedData, Quarantine: quarantine}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(active, quarantine); err != nil {
		t.Fatal(err)
	}
	if err := managed.recoverSwitch(); err != nil {
		t.Fatalf("recover interrupted active switch: %v", err)
	}
	if err := validateActiveCopy(active); err != nil {
		t.Fatalf("recovered active data: %v", err)
	}
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("previous active data was not quarantined: %v", err)
	}
	unlock, err = Start(firstHash.String())
	if err != nil {
		t.Fatalf("repeated startup: %v", err)
	}
	unlock()
	importedActive := filepath.Join(root, "imported-active")
	if err := os.MkdirAll(importedActive, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(importedActive, "old-development-data.txt"), []byte("quarantine me"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_ROOT", importedActive)
	t.Setenv("DEV_BASELINE_SOURCE", base)
	config.Init()
	unlock, err = Start(firstHash.String())
	if err != nil {
		t.Fatalf("import released backup with existing active data: %v", err)
	}
	unlock()
	if !hasProbeColumn(t, importedActive) {
		t.Fatal("imported release baseline did not advance through dev.1")
	}
	quarantined, err := filepath.Glob(filepath.Join(importedActive+".developer-data", "quarantine", "*", "old-development-data.txt"))
	if err != nil || len(quarantined) != 1 {
		t.Fatalf("old active data was not preserved in quarantine: %v, %v", quarantined, err)
	}
	t.Setenv("DATA_ROOT", active)
	t.Setenv("DEV_BASELINE_SOURCE", "")
	config.Init()

	db, err := openDataDB(active)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE local_test_data (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if err := closeDataDB(db); err != nil {
		t.Fatal(err)
	}
	writeSource("rewritten development step A")
	rewrittenHash := commit("rewrite existing dev.1 SQL")
	if unlock, err := Start(rewrittenHash.String()); err == nil {
		unlock()
		t.Fatal("rewritten dev.1 SQL was accepted as the original step")
	}
	if state, err := loadState(active); err != nil || state.DevCommit != firstHash.String() {
		t.Fatalf("rejected rewrite changed active state: %+v, %v", state, err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("rebased"), Create: true, Hash: releaseHash,
	}); err != nil {
		t.Fatal(err)
	}
	writeSource("development step B, same revision")
	secondHash := commit("dev.1 B")
	unlock, err = Start(secondHash.String())
	if err != nil {
		t.Fatalf("rebuild after rebase: %v", err)
	}
	unlock()
	state, err = loadState(active)
	if err != nil || state.DevCommit != secondHash.String() {
		t.Fatalf("rebuilt state = %+v, error = %v", state, err)
	}
	db, err = openDataDB(active)
	if err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable("local_test_data") {
		t.Fatal("rebase reused old active data despite a changed dev.1 commit")
	}
	if err := closeDataDB(db); err != nil {
		t.Fatal(err)
	}
}

// TestRejectedCheckoutLeavesStoreUntouched proves that source verification runs
// before any data mutation: a dirty worktree must not create a baseline, seed
// active data, or leave staging artifacts behind.
func TestRejectedCheckoutLeavesStoreUntouched(t *testing.T) {
	root := t.TempDir()
	repoPath := filepath.Join(root, "source")
	if err := os.MkdirAll(repoPath, 0700); err != nil {
		t.Fatal(err)
	}
	repo, err := git.PlainInit(repoPath, false)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	commit := func(message string) plumbing.Hash {
		t.Helper()
		hash, err := worktree.Commit(message, &git.CommitOptions{Author: &object.Signature{
			Name: "Developer Data Test", Email: "test@example.invalid", When: time.Now(),
		}})
		if err != nil {
			t.Fatal(err)
		}
		return hash
	}
	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("released"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	releaseHash := commit("release")
	if _, err := repo.CreateTag("v0.1.21", releaseHash, nil); err != nil {
		t.Fatal(err)
	}
	source := "server/internal/migration/dev/unreleased/scripts/1.sql"
	sourcePath := filepath.Join(repoPath, source)
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("development step"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add(source); err != nil {
		t.Fatal(err)
	}
	devHash := commit("dev.1")
	// An untracked file alone must reject the checkout.
	if err := os.WriteFile(filepath.Join(repoPath, "scratch.txt"), []byte("local"), 0600); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(root, "data")
	t.Setenv("DATA_ROOT", active)
	t.Setenv("LOG_DIR", filepath.Join(root, "logs"))
	t.Setenv("DEV_DATA_MODE", "1")
	t.Setenv("DEV_REPO_ROOT", repoPath)
	config.Init()
	if unlock, err := Start(devHash.String()); err == nil {
		unlock()
		t.Fatal("dirty worktree was accepted")
	}
	store := active + ".developer-data"
	if _, err := os.Stat(filepath.Join(store, "baseline")); !os.IsNotExist(err) {
		t.Fatalf("rejected start created a baseline: %v", err)
	}
	if _, err := os.Stat(active); !os.IsNotExist(err) {
		t.Fatalf("rejected start created active data: %v", err)
	}
	staged, err := os.ReadDir(filepath.Join(store, "staging"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(staged) != 0 {
		t.Fatalf("rejected start left %d staging entries", len(staged))
	}
}

func hasProbeColumn(t *testing.T, root string) bool {
	t.Helper()
	db, err := openDataDB(root)
	if err != nil {
		t.Fatal(err)
	}
	has := db.Migrator().HasColumn("llm_configs", "probe_status")
	if err := closeDataDB(db); err != nil {
		t.Fatal(err)
	}
	return has
}

func hasProbeColumnImmutable(t *testing.T, root string) bool {
	t.Helper()
	path := filepath.Join(root, "db", "database.db")
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM pragma_table_info('llm_configs') WHERE name = 'probe_status'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count != 0
}
