package testrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/migration"
	"qingqiu-world-server/internal/migration/dev/unreleased"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/world"
)

// TestReleasedDataMigratesOnceWithoutTestTables checks real released schema,
// unreleased SQL/Go execution, and a second preparation against the same data.
// Process restart itself is covered by the existing D restart cases.
func TestReleasedDataMigratesOnceWithoutTestTables(t *testing.T) {
	runRoot := t.TempDir()
	root := filepath.Join(runRoot, "product")
	statePath := filepath.Join(runRoot, "test-migrations.json")
	t.Setenv("DATA_ROOT", root)
	for _, key := range []string{"AOS_ROOT", "AOS_META_ROOT", "JINSHU_ROOT"} {
		t.Setenv(key, "")
	}
	config.Init()
	runner, err := Open(statePath, root)
	if err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.Init()
	db := database.DB
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		database.DB = previous
	})
	if err := os.WriteFile(filepath.Join(root, "tz.txt"), []byte("UTC"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := world.InitTime(db, root); err != nil {
		t.Fatal(err)
	}
	migration.Run()
	before, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasColumn("llm_configs", "probe_status") {
		t.Fatal("released initialization already contains unreleased columns")
	}
	chat := model.Action{DecisionID: 1, Type: 0}
	if err := db.Create(&chat).Error; err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(db, root); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn("llm_configs", "probe_status") {
		t.Fatal("test startup did not apply the registered schema change")
	}
	if err := db.First(&chat, chat.ID).Error; err != nil || chat.Type != model.ActionTypeChat {
		t.Fatalf("Action conversion = %+v, error = %v", chat, err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state progress
	if err := json.Unmarshal(data, &state); err != nil || state.BaseRelease != config.AppVersion || state.Revision != len(unreleased.Registered()) {
		t.Fatalf("test progress = %s, error = %v", data, err)
	}
	// A business write must survive the next startup, while non-idempotent
	// ADD COLUMN statements must not run a second time.
	person := model.Person{Name: "restart owner", Type: model.PersonTypeHuman}
	if err := db.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(statePath, root)
	if err != nil {
		t.Fatal(err)
	}
	migration.Run()
	if err := restarted.Apply(db, root); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&person, person.ID).Error; err != nil || person.Name != "restart owner" {
		t.Fatalf("business data changed: %+v, error = %v", person, err)
	}
	after, err := db.Migrator().GetTables()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("test infrastructure changed product tables: before=%v after=%v error=%v", before, after, err)
	}
	var versions []model.DBVersion
	if err := db.Find(&versions).Error; err != nil || len(versions) != 1 || versions[0].Version != config.AppVersion {
		t.Fatalf("release history changed: %+v, error = %v", versions, err)
	}
	for _, path := range []string{filepath.Join(root, "test-migrations.json"), root + ".developer-data", filepath.Join(root, ".dev-data-version.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected test/developer state at %s: %v", path, err)
		}
	}
}

// TestOpenRejectsUnusableProgressBeforeProductCreation checks only the input
// boundary; failed test data is never repaired or rolled back.
func TestOpenRejectsUnusableProgressBeforeProductCreation(t *testing.T) {
	for _, tc := range []struct {
		name, content string
	}{
		{"invalid JSON", "{"},
		{"wrong release", `{"base_release":"0.0.0","revision":0}`},
		{"negative revision", `{"base_release":"` + config.AppVersion + `","revision":-1}`},
		{"unknown revision", `{"base_release":"` + config.AppVersion + `","revision":999}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runRoot := t.TempDir()
			root := filepath.Join(runRoot, "product")
			statePath := filepath.Join(runRoot, "test-migrations.json")
			if err := os.WriteFile(statePath, []byte(tc.content), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(statePath, root); err == nil {
				t.Fatal("invalid test progress was accepted")
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("product data was created: %v", err)
			}
		})
	}
	root := t.TempDir()
	for _, path := range []string{"", "relative.json", filepath.Join(root, "test-migrations.json")} {
		if _, err := Open(path, root); err == nil {
			t.Fatalf("progress inside product data or relative path was accepted: %q", path)
		}
	}
}
