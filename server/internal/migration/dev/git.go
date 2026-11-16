package dev

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Repository is a checked-out source tree used for developer data identity.
type Repository struct {
	root string
	repo *git.Repository
}

// Open reads a checkout, including linked worktrees whose .git is a file.
func Open(root string) (*Repository, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository path: %w", err)
	}
	repo, err := git.PlainOpenWithOptions(abs, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return nil, fmt.Errorf("open Git checkout %s: %w", abs, err)
	}
	return &Repository{root: abs, repo: repo}, nil
}

// Root returns the explicit checkout directory.
func (r *Repository) Root() string { return r.root }

// Head returns the full checked-out commit identity.
func (r *Repository) Head() (*object.Commit, error) {
	ref, err := r.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("read Git HEAD: %w", err)
	}
	commit, err := r.repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("read Git HEAD commit: %w", err)
	}
	return commit, nil
}

// HeadHash returns the full hash for build stamping.
func (r *Repository) HeadHash() (string, error) {
	commit, err := r.Head()
	if err != nil {
		return "", err
	}
	return commit.Hash.String(), nil
}

// Clean reports whether tracked or untracked source changes are present.
func (r *Repository) Clean() error {
	worktree, err := r.repo.Worktree()
	if err != nil {
		return fmt.Errorf("read Git worktree: %w", err)
	}
	status, err := worktree.Status()
	if err != nil {
		return fmt.Errorf("read Git worktree status: %w", err)
	}
	if !status.IsClean() {
		return fmt.Errorf("uncommitted source changes prevent automatic developer data migration")
	}
	return nil
}

// ReleaseCommit resolves a release tag to a full commit, including annotated tags.
func (r *Repository) ReleaseCommit(version string) (*object.Commit, error) {
	hash, err := r.repo.ResolveRevision(plumbing.Revision("refs/tags/v" + version))
	if err != nil {
		return nil, fmt.Errorf("resolve release tag v%s: %w", version, err)
	}
	commit, err := r.repo.CommitObject(*hash)
	if err != nil {
		return nil, fmt.Errorf("read release commit v%s: %w", version, err)
	}
	return commit, nil
}

// FirstParentPath returns commits after ancestor through HEAD in execution
// order. Nonlinear histories are rejected when they obscure a migration step.
func (r *Repository) FirstParentPath(ancestor, head *object.Commit) ([]*object.Commit, error) {
	var reverse []*object.Commit
	for current := head; current.Hash != ancestor.Hash; {
		if current.NumParents() == 0 {
			return nil, fmt.Errorf("release commit %s is not an ancestor of HEAD %s", ancestor.Hash, head.Hash)
		}
		reverse = append(reverse, current)
		parent, err := current.Parent(0)
		if err != nil {
			return nil, fmt.Errorf("read parent of %s: %w", current.Hash, err)
		}
		current = parent
	}
	for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
		reverse[i], reverse[j] = reverse[j], reverse[i]
	}
	return reverse, nil
}

// IntroducingCommit finds the unique commit that added a migration source.
// Its hash is read from history after the migration has been committed.
func (r *Repository) IntroducingCommit(path string, base, head *object.Commit) (string, error) {
	commits, err := r.FirstParentPath(base, head)
	if err != nil {
		return "", err
	}
	previous := base
	var introduced string
	var introducedBlob plumbing.Hash
	for _, commit := range commits {
		_, beforeErr := previous.File(path)
		file, afterErr := commit.File(path)
		if beforeErr != nil && !errors.Is(beforeErr, object.ErrFileNotFound) {
			return "", fmt.Errorf("inspect %s at %s: %w", path, previous.Hash, beforeErr)
		}
		if afterErr != nil && !errors.Is(afterErr, object.ErrFileNotFound) {
			return "", fmt.Errorf("inspect %s at %s: %w", path, commit.Hash, afterErr)
		}
		if errors.Is(beforeErr, object.ErrFileNotFound) && afterErr == nil {
			if commit.NumParents() != 1 || introduced != "" {
				return "", fmt.Errorf("migration source %s has ambiguous introduction at %s", path, commit.Hash)
			}
			introduced = commit.Hash.String()
			introducedBlob = file.Hash
		}
		if beforeErr == nil && errors.Is(afterErr, object.ErrFileNotFound) {
			return "", fmt.Errorf("migration source %s was removed at %s", path, commit.Hash)
		}
		if introduced != "" && afterErr == nil && file.Hash != introducedBlob {
			return "", fmt.Errorf("migration source %s was rewritten after introduction at %s; rebase or add a compatible new revision", path, commit.Hash)
		}
		previous = commit
	}
	if introduced == "" {
		return "", fmt.Errorf("migration source %s was not introduced after release %s", path, base.Hash)
	}
	return introduced, nil
}

// ValidateRegisteredChanges rejects known persistent-code changes left after
// the last declared development step. A step may cover earlier commits in the
// same development sequence, but an intermediate checkout will fail until it
// includes that step. Semantic changes outside these known paths still need
// developer registration and review.
func (r *Repository) ValidateRegisteredChanges(base, head *object.Commit, stepCommits map[string]bool) error {
	commits, err := r.FirstParentPath(base, head)
	if err != nil {
		return err
	}
	pending := make(map[string]bool)
	for _, commit := range commits {
		stats, err := commit.Stats()
		if err != nil {
			return fmt.Errorf("inspect changes at %s: %w", commit.Hash, err)
		}
		for _, stat := range stats {
			if knownPersistentSource(stat.Name) {
				pending[stat.Name] = true
			}
		}
		if stepCommits[commit.Hash.String()] {
			clear(pending)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	paths := make([]string, 0, len(pending))
	for path := range pending {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return fmt.Errorf("persistent code changes lack a later development migration: %s", strings.Join(paths, ", "))
}

// knownPersistentSource reports whether a path carries persistent data
// behaviour. Documentation and placeholder files under these directories are
// excluded: editing a guide must not demand a development migration.
func knownPersistentSource(path string) bool {
	if strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".md") || strings.HasPrefix(filepath.Base(path), ".") {
		return false
	}
	return strings.HasPrefix(path, "server/internal/model/") ||
		strings.HasPrefix(path, "server/internal/migration/") ||
		path == "server/internal/config/config.go" ||
		strings.HasPrefix(path, "server/internal/service/aos/") ||
		strings.HasPrefix(path, "server/internal/service/jinshu/") ||
		path == "server/internal/service/speech/storage.go"
}
