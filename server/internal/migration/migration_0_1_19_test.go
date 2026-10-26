package migration

import (
	"os"
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestWorkspaceMigrationOnlyRegistersExistingPaths verifies that the upgrade
// preserves real legacy files and never fabricates a directory or its origin.
func TestWorkspaceMigrationOnlyRegistersExistingPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("AOS_ROOT", filepath.Join(root, "aos"))
	config.Init()
	previous := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(root, "upgrade.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	if err := db.AutoMigrate(&model.Person{}, &model.Work{}, &model.Workspace{}, &model.WorkspaceUse{}, &model.Action{}, &model.AgentEventBuffer{}, &model.Event{}, &model.DBVersion{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Person{ID: 51, Name: "agent", Type: model.PersonTypeAI}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Work{ID: 71, PersonID: 51, SessionID: 12, Description: "old work"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Work{ID: 72, PersonID: 51, SessionID: 13, Description: "no files yet"}).Error; err != nil {
		t.Fatal(err)
	}
	legacy := legacySessionWorkspacePath(51, 12)
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "real.txt"), []byte("history"), 0644); err != nil {
		t.Fatal(err)
	}
	legacyMeta := legacySessionMetaDir(51, 12)
	if err := os.MkdirAll(legacyMeta, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyMeta, "notes.jsonl"), []byte("existing notes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	migrate_0_1_19()
	migrate_0_1_19()
	var registered []model.Workspace
	if err := db.Find(&registered).Error; err != nil {
		t.Fatal(err)
	}
	if len(registered) != 1 || registered[0].RelativePath != filepath.Join("work", "12") {
		t.Fatalf("registered = %+v", registered)
	}
	var uses []model.WorkspaceUse
	if err := db.Find(&uses).Error; err != nil {
		t.Fatal(err)
	}
	if len(uses) != 1 || uses[0].SourceID != 71 || uses[0].WorkspaceID != registered[0].ID {
		t.Fatalf("uses = %+v", uses)
	}
	if content, err := os.ReadFile(filepath.Join(legacy, "real.txt")); err != nil || string(content) != "history" {
		t.Fatalf("legacy file changed: %q %v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(legacyMeta, "notes.jsonl")); err != nil || string(content) != "existing notes\n" {
		t.Fatalf("legacy metadata changed: %q %v", content, err)
	}
	if _, err := os.Stat(legacySessionWorkspacePath(51, 13)); !os.IsNotExist(err) {
		t.Fatalf("missing directory was created: %v", err)
	}
}
