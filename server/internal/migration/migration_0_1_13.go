package migration

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"qingqiu-world-server/internal/config"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/service/workspace"
)

// migrate_0_1_13 copies legacy filesystem data into the Agent Owned Space
// layout. A failed copy aborts this versioned migration, so its DB version is
// not recorded and the complete copy can safely be retried on the next start.
func migrate_0_1_13() {
	if err := migrateLegacyOwnedSpace(); err != nil {
		applogger.Error("migration 0.1.13: legacy Agent Owned Space import failed", "error", err)
		panic(fmt.Sprintf("migration 0.1.13: %v", err))
	}
}

// migrateLegacyOwnedSpace discovers all legacy person/session directories and
// invokes the copy-only import operations provided by workspace. Discovery and
// release-version ownership belong here; workspace owns path resolution and
// one-directory copy semantics.
func migrateLegacyOwnedSpace() error {
	workspaceRoot := legacyRoot("WORKSPACE_ROOT", filepath.Join(config.Get().GetDataRoot(), "workspace"))
	privateRoot := legacyRoot("PRIVATE_SPACE_ROOT", filepath.Join(config.Get().GetDataRoot(), "private_space"))
	personIDs, err := legacyDirectoryIDs(workspaceRoot)
	if err != nil {
		return fmt.Errorf("list legacy workspace persons: %w", err)
	}
	privatePersonIDs, err := legacyDirectoryIDs(privateRoot)
	if err != nil {
		return fmt.Errorf("list legacy private-space persons: %w", err)
	}
	for personID := range privatePersonIDs {
		personIDs[personID] = struct{}{}
	}

	var migrationErrs []error
	for _, personID := range sortedIDs(personIDs) {
		if err := migrateLegacyPrivateSpace(personID, privateRoot); err != nil {
			migrationErrs = append(migrationErrs, fmt.Errorf("import legacy private space for person %d: %w", personID, err))
		}

		sessionIDs, err := legacyDirectoryIDs(filepath.Join(workspaceRoot, strconv.FormatInt(personID, 10)))
		if err != nil {
			migrationErrs = append(migrationErrs, fmt.Errorf("list legacy sessions for person %d: %w", personID, err))
			continue
		}
		for _, sessionID := range sortedIDs(sessionIDs) {
			if err := migrateLegacyWorkspace(personID, sessionID, workspaceRoot); err != nil {
				migrationErrs = append(migrationErrs, fmt.Errorf("import legacy workspace for person %d session %d: %w", personID, sessionID, err))
			}
		}
	}
	if len(migrationErrs) > 0 {
		return errors.Join(migrationErrs...)
	}
	return nil
}

// legacyRoot resolves one historical environment override without admitting it
// into the current runtime configuration. The fallback is the exact directory
// contract used by the pre-AOS runtime.
func legacyRoot(envKey, fallback string) string {
	path := strings.TrimSpace(os.Getenv(envKey))
	if path == "" {
		path = fallback
	}
	if path == "~" || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			applogger.Error("migration 0.1.13: resolve legacy home directory failed", "environment_key", envKey, "error", err)
		} else if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~"+string(filepath.Separator)))
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		applogger.Error("migration 0.1.13: resolve legacy path failed", "environment_key", envKey, "path", path, "error", err)
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

// legacyDirectoryIDs reads one numeric legacy hierarchy level. Invalid entries
// are logged and ignored because historical data roots can contain unrelated
// operational files; unreadable directories remain migration failures.
func legacyDirectoryIDs(root string) (map[int64]struct{}, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return map[int64]struct{}{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := make(map[int64]struct{}, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			applogger.Warn("migration 0.1.13: ignoring non-directory legacy entry", "root", root, "name", entry.Name())
			continue
		}
		id, err := strconv.ParseInt(entry.Name(), 10, 64)
		if err != nil || id <= 0 {
			applogger.Warn("migration 0.1.13: ignoring invalid legacy directory", "root", root, "name", entry.Name())
			continue
		}
		ids[id] = struct{}{}
	}
	return ids, nil
}

// sortedIDs produces deterministic migration order for predictable logs.
func sortedIDs(ids map[int64]struct{}) []int64 {
	result := make([]int64, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// migrateLegacyWorkspace imports one historical session directory into its AOS
// and AOSMeta targets. It is strictly copy-only and records completion only
// after both resource and metadata copies finish.
func migrateLegacyWorkspace(personID, sessionID int64, legacyRoot string) error {
	legacy := filepath.Join(legacyRoot, strconv.FormatInt(personID, 10), strconv.FormatInt(sessionID, 10))
	targetWorkspace := workspace.GetWorkspacePath(personID, sessionID)
	targetMeta := workspace.GetMetaDir(personID, sessionID)
	recorded, err := migrationAlreadyRecorded(filepath.Join(targetMeta, "legacy_workspace_imported"))
	if err != nil {
		return fmt.Errorf("check legacy workspace migration marker: %w", err)
	}
	if recorded {
		return nil
	}
	if _, err := os.Stat(legacy); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat legacy workspace: %w", err)
	}
	if err := copyTreeMissing(legacy, targetWorkspace, true); err != nil {
		return fmt.Errorf("copy legacy resources: %w", err)
	}
	if err := copyTreeMissing(filepath.Join(legacy, ".meta"), targetMeta, false); err != nil {
		return fmt.Errorf("copy legacy metadata: %w", err)
	}
	if err := recordMigration(filepath.Join(targetMeta, "legacy_workspace_imported")); err != nil {
		return fmt.Errorf("record legacy workspace migration: %w", err)
	}
	applogger.Info("migration 0.1.13: legacy workspace imported", "person_id", personID, "session_id", sessionID, "legacy_path", legacy, "target_path", targetWorkspace)
	return nil
}

// migrateLegacyPrivateSpace imports one historical private directory into AOS
// and AOSMeta with the same copy-only completion semantics.
func migrateLegacyPrivateSpace(personID int64, legacyRoot string) error {
	legacy := filepath.Join(legacyRoot, strconv.FormatInt(personID, 10))
	targetWork := workspace.GetPrivateSpacePath(personID)
	targetMeta := workspace.GetPrivateMetaDir(personID)
	recorded, err := migrationAlreadyRecorded(filepath.Join(targetMeta, "legacy_private_imported"))
	if err != nil {
		return fmt.Errorf("check legacy private migration marker: %w", err)
	}
	if recorded {
		return nil
	}
	if _, err := os.Stat(legacy); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat legacy private space: %w", err)
	}
	if err := copyTreeMissing(filepath.Join(legacy, "space"), targetWork, false); err != nil {
		return fmt.Errorf("copy legacy private resources: %w", err)
	}
	if err := copyTreeMissing(filepath.Join(legacy, "log.jsonl"), filepath.Join(targetMeta, "log.jsonl"), false); err != nil {
		return fmt.Errorf("copy legacy private log: %w", err)
	}
	if err := recordMigration(filepath.Join(targetMeta, "legacy_private_imported")); err != nil {
		return fmt.Errorf("record legacy private migration: %w", err)
	}
	applogger.Info("migration 0.1.13: legacy private space imported", "person_id", personID, "legacy_path", legacy, "target_path", targetWork)
	return nil
}

// migrationAlreadyRecorded distinguishes an absent marker from an I/O failure.
func migrationAlreadyRecorded(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// recordMigration writes the marker only after a complete legacy copy.
func recordMigration(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("imported\n"), 0644)
}

// copyTreeMissing recursively copies a source tree without overwriting an AOS
// entry that already exists. skipRootMeta keeps legacy workspace metadata out
// of the resource tree so it can be copied to AOSMeta separately.
func copyTreeMissing(src, dst string, skipRootMeta bool) error {
	info, err := os.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return copyEntryMissing(src, dst, info)
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if skipRootMeta && entry.Name() == ".meta" {
			continue
		}
		childSrc := filepath.Join(src, entry.Name())
		childDst := filepath.Join(dst, entry.Name())
		childInfo, childErr := os.Lstat(childSrc)
		if childErr != nil {
			return childErr
		}
		if childInfo.IsDir() {
			if err := copyTreeMissing(childSrc, childDst, false); err != nil {
				return err
			}
			continue
		}
		if err := copyEntryMissing(childSrc, childDst, childInfo); err != nil {
			return err
		}
	}
	return nil
}

// copyEntryMissing copies one absent file or symlink without replacing a target.
func copyEntryMissing(src, dst string, info os.FileInfo) error {
	if _, err := os.Lstat(dst); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		if closeErr := in.Close(); closeErr != nil {
			applogger.Error("migration 0.1.13: close source after destination open failure", "source", src, "error", closeErr)
		}
		return err
	}
	_, copyErr := io.Copy(out, in)
	inCloseErr := in.Close()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if inCloseErr != nil {
		return inCloseErr
	}
	return closeErr
}
