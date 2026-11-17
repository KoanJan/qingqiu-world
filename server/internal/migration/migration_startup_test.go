package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func startupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	config.Init()
	if err := os.MkdirAll(filepath.Join(root, "db"), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(root, "db", "database.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	return db
}

func TestFreshDatabaseInitializesCurrentReleaseOnce(t *testing.T) {
	db := startupTestDB(t)
	Run()
	Run()
	if got := getDBVersion(); got != config.AppVersion {
		t.Fatalf("database version = %q", got)
	}
	var versions, defaults int64
	if err := db.Model(&model.DBVersion{}).Count(&versions).Error; err != nil || versions != 1 {
		t.Fatalf("version records = %d, error = %v", versions, err)
	}
	if err := db.Model(&model.SearchConfig{}).Count(&defaults).Error; err != nil || defaults != 1 {
		t.Fatalf("default search configs = %d, error = %v", defaults, err)
	}
	for _, table := range []string{"interactions", "kb_access", "cognitive_memories"} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("fresh database missing %s", table)
		}
	}
	if db.Migrator().HasColumn("llm_configs", "probe_status") {
		t.Fatal("fresh released database contains unreleased LLM probe columns")
	}
	if !db.Migrator().HasIndex(&model.Decision{}, "idx_decisions_external_event") {
		t.Fatal("fresh database missing partial decision index")
	}
}

func TestNonemptyDatabaseRequiresValidVersion(t *testing.T) {
	for _, tc := range []struct {
		name, version, message string
		createVersionTable     bool
	}{
		{"missing table", "", "no db_versions table", false},
		{"missing record", "", "no version record", true},
		{"malformed", "0.1.bad", "invalid database version", true},
		{"future", "0.1.23", "newer than application", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := startupTestDB(t)
			if err := db.Exec("CREATE TABLE sentinel (id INTEGER PRIMARY KEY, payload TEXT NOT NULL)").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("INSERT INTO sentinel (id, payload) VALUES (1, 'preserve me')").Error; err != nil {
				t.Fatal(err)
			}
			if tc.createVersionTable {
				if err := db.Migrator().CreateTable(&model.DBVersion{}); err != nil {
					t.Fatal(err)
				}
				if tc.version != "" {
					if err := db.Create(&model.DBVersion{Version: tc.version}).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			func() {
				defer func() {
					if cause := recover(); cause == nil || !strings.Contains(fmt.Sprint(cause), tc.message) {
						t.Fatalf("expected panic containing %q, got %v", tc.message, cause)
					}
				}()
				Run()
			}()
			var payload string
			if err := db.Raw("SELECT payload FROM sentinel WHERE id = 1").Scan(&payload).Error; err != nil || payload != "preserve me" {
				t.Fatalf("existing data changed: payload=%q error=%v", payload, err)
			}
		})
	}
}

func TestOutOfOrderVersionHistoryRejected(t *testing.T) {
	db := startupTestDB(t)
	if err := db.Exec("CREATE TABLE sentinel (id INTEGER PRIMARY KEY, payload TEXT NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO sentinel (id, payload) VALUES (1, 'preserve me')").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().CreateTable(&model.DBVersion{}); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"0.1.21", "0.1.6"} {
		if err := db.Create(&model.DBVersion{Version: version}).Error; err != nil {
			t.Fatal(err)
		}
	}
	func() {
		defer func() {
			if cause := recover(); cause == nil || !strings.Contains(fmt.Sprint(cause), "out of order") {
				t.Fatalf("expected out-of-order history rejection, got %v", cause)
			}
		}()
		Run()
	}()
	var payload string
	if err := db.Raw("SELECT payload FROM sentinel WHERE id = 1").Scan(&payload).Error; err != nil || payload != "preserve me" {
		t.Fatalf("existing data changed: payload=%q error=%v", payload, err)
	}
}

func TestVersionHistoryAllowsRepeatedReleaseMarkers(t *testing.T) {
	records := []versionRecord{
		{ID: 1, Version: "0.1.19"},
		{ID: 2, Version: "0.1.19"},
		{ID: 3, Version: "0.1.19"},
		{ID: 4, Version: "0.1.20"},
	}
	version, err := validateVersionHistory(records)
	if err != nil || version != "0.1.20" {
		t.Fatalf("version = %q, error = %v", version, err)
	}
}

func TestExistingDataFilesDoNotBecomeFreshInstall(t *testing.T) {
	db := startupTestDB(t)
	asset := filepath.Join(config.Get().DataRoot, "avatars", "existing.png")
	if err := os.MkdirAll(filepath.Dir(asset), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(asset, []byte("existing user data"), 0644); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if cause := recover(); cause == nil || !strings.Contains(fmt.Sprint(cause), "nonempty data directory") {
				t.Fatalf("expected nonempty data directory rejection, got %v", cause)
			}
		}()
		Run()
	}()
	if _, err := os.Stat(asset); err != nil || db.Migrator().HasTable(&model.DBVersion{}) {
		t.Fatalf("existing data was modified or a new schema was created: %v", err)
	}
}

func TestSystemMetadataDoesNotBlockFreshInstall(t *testing.T) {
	db := startupTestDB(t)
	for _, path := range []string{
		filepath.Join(config.Get().DataRoot, ".DS_Store"),
		filepath.Join(config.Get().DataRoot, "._.DS_Store"),
		filepath.Join(config.Get().DataRoot, "db", ".DS_Store"),
		filepath.Join(config.Get().DataRoot, "db", "Thumbs.db"),
	} {
		if err := os.WriteFile(path, []byte("system metadata"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	Run()
	if got := getDBVersion(); got != config.AppVersion {
		t.Fatalf("fresh install version = %q, want %q", got, config.AppVersion)
	}
	if !db.Migrator().HasTable("db_versions") {
		t.Fatal("fresh installation did not create version table")
	}
}

func TestSystemMetadataNameDoesNotHideDirectory(t *testing.T) {
	db := startupTestDB(t)
	if err := os.Mkdir(filepath.Join(config.Get().DataRoot, ".DS_Store"), 0755); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cause := recover(); cause == nil || !strings.Contains(fmt.Sprint(cause), "nonempty data directory") {
			t.Fatalf("expected nonempty data directory rejection, got %v", cause)
		}
		if db.Migrator().HasTable("db_versions") {
			t.Fatal("system metadata directory was treated as an empty install")
		}
	}()
	Run()
}

func TestConfirmedPre010DatabaseIsReset(t *testing.T) {
	db := startupTestDB(t)
	if err := db.Migrator().CreateTable(&model.DBVersion{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE sentinel (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.DBVersion{Version: "0.0.9"}).Error; err != nil {
		t.Fatal(err)
	}
	Run()
	if db.Migrator().HasTable("sentinel") || getDBVersion() != config.AppVersion {
		t.Fatal("confirmed pre-0.1.0 database was not replaced with current schema")
	}
}

func TestJinshuReleaseMigrationPreservesDeliveryHistory(t *testing.T) {
	db := startupTestDB(t)
	if err := db.Exec(`CREATE TABLE agent_deliveries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		from_person_id INTEGER NOT NULL,
		to_person_id INTEGER NOT NULL,
		session_id INTEGER NOT NULL,
		paths TEXT NOT NULL,
		remark TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO agent_deliveries
		(id, from_person_id, to_person_id, session_id, paths, remark, created_at)
		VALUES (17, 1, 2, 5, '[]', 'historical note', CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return applyReleaseSQL(tx, "0.1.7")
	}); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable("agent_deliveries") {
		t.Fatal("legacy delivery table still exists")
	}
	var jinshus []model.Jinshu
	if err := db.Find(&jinshus).Error; err != nil || len(jinshus) != 1 ||
		jinshus[0].ID != 17 || jinshus[0].Description != "historical note" {
		t.Fatalf("migrated deliveries = %+v, error = %v", jinshus, err)
	}
}

func TestUpgradeFromLatestTagAddsProbeColumns(t *testing.T) {
	db := startupTestDB(t)
	if err := db.Migrator().CreateTable(&model.DBVersion{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE llm_configs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name varchar(100) NOT NULL,
		model_id varchar(100) NOT NULL,
		base_url varchar(255) NOT NULL,
		api_key varchar(255) NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		created_at datetime NOT NULL,
		updated_at datetime NOT NULL
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO llm_configs
		(id, name, model_id, base_url, api_key, created_at, updated_at)
		VALUES (7, 'existing', 'model', 'https://example.test', 'secret', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.DBVersion{Version: "0.1.21"}).Error; err != nil {
		t.Fatal(err)
	}
	Run()
	var configRow model.LLMConfig
	if err := db.First(&configRow, 7).Error; err != nil || configRow.Name != "existing" ||
		configRow.ProbeStatus != model.ProbeStatusUnverified {
		t.Fatalf("upgraded LLM config id=%d name=%q probe_status=%d, error=%v",
			configRow.ID, configRow.Name, configRow.ProbeStatus, err)
	}
	if getDBVersion() != config.AppVersion {
		t.Fatal("probe schema upgrade did not record the current version")
	}
}
