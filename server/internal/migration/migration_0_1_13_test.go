package migration

import (
	"os"
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/config"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/service/workspace"
)

// TestLegacyOwnedSpaceMigrationIsRegistered verifies an upgrade to the current
// release executes the filesystem migration through migration.Run.
func TestLegacyOwnedSpaceMigrationIsRegistered(t *testing.T) {
	for _, item := range migrations {
		if item.version == config.AppVersion && item.fn != nil {
			return
		}
	}
	t.Fatalf("no migration registered for current version %s", config.AppVersion)
}

// TestMigrateLegacyOwnedSpaceCopiesWithoutDeletingOrOverwriting verifies the
// versioned migration copies all resource and metadata surfaces, preserves the
// old data, and remains safe when its completed snapshot is invoked again.
func TestMigrateLegacyOwnedSpaceCopiesWithoutDeletingOrOverwriting(t *testing.T) {
	root := t.TempDir()
	legacyWorkspaceRoot := filepath.Join(root, "legacy-workspace")
	legacyPrivateRoot := filepath.Join(root, "legacy-private")
	t.Setenv("DATA_ROOT", root)
	t.Setenv("AOS_ROOT", filepath.Join(root, "aos"))
	t.Setenv("AOS_META_ROOT", filepath.Join(root, "aosmeta"))
	t.Setenv("WORKSPACE_ROOT", legacyWorkspaceRoot)
	t.Setenv("PRIVATE_SPACE_ROOT", legacyPrivateRoot)
	config.Init()
	applogger.Init()

	const personID int64 = 61
	const sessionID int64 = 67
	legacySession := filepath.Join(legacyWorkspaceRoot, "61", "67")
	writeMigrationFixture(t, filepath.Join(legacySession, "artifact.txt"), "resource")
	writeMigrationFixture(t, filepath.Join(legacySession, ".meta", "notes.jsonl"), "metadata\n")
	legacyPrivate := filepath.Join(legacyPrivateRoot, "61")
	writeMigrationFixture(t, filepath.Join(legacyPrivate, "space", "idea.txt"), "idea")
	writeMigrationFixture(t, filepath.Join(legacyPrivate, "log.jsonl"), "entry\n")

	if err := migrateLegacyOwnedSpace(); err != nil {
		t.Fatalf("migrate legacy owned space: %v", err)
	}
	assertMigrationFile(t, filepath.Join(workspace.GetWorkspacePath(personID, sessionID), "artifact.txt"), "resource")
	assertMigrationFile(t, filepath.Join(workspace.GetMetaDir(personID, sessionID), "notes.jsonl"), "metadata\n")
	assertMigrationFile(t, filepath.Join(workspace.GetPrivateSpacePath(personID), "idea.txt"), "idea")
	assertMigrationFile(t, filepath.Join(workspace.GetPrivateMetaDir(personID), "log.jsonl"), "entry\n")

	// The source must remain a complete recovery copy after the migration.
	assertMigrationFile(t, filepath.Join(legacySession, "artifact.txt"), "resource")
	assertMigrationFile(t, filepath.Join(legacyPrivate, "space", "idea.txt"), "idea")

	// Even if a previous process stopped before retaining its completion marker,
	// the copy-only primitive must never overwrite newer AOS data.
	writeMigrationFixture(t, filepath.Join(workspace.GetWorkspacePath(personID, sessionID), "artifact.txt"), "newer-resource")
	if err := os.Remove(filepath.Join(workspace.GetMetaDir(personID, sessionID), "legacy_workspace_imported")); err != nil {
		t.Fatalf("remove workspace migration marker: %v", err)
	}
	if err := migrateLegacyOwnedSpace(); err != nil {
		t.Fatalf("repeat legacy owned-space migration: %v", err)
	}
	assertMigrationFile(t, filepath.Join(workspace.GetWorkspacePath(personID, sessionID), "artifact.txt"), "newer-resource")
}

func writeMigrationFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create fixture parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

func assertMigrationFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("file content mismatch for %s: data=%q err=%v", path, data, err)
	}
}
