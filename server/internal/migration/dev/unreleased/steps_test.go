package unreleased

import (
	"path/filepath"
	"strings"
	"testing"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestDevelopmentSQLRollsBackOnLaterStatementFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "database.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.InitializeFreshAt(db, config.AppVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE llm_configs ADD COLUMN probe_required_tools_ok numeric NOT NULL DEFAULT false").Error; err != nil {
		t.Fatal(err)
	}
	err = Apply(db, 1, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "execute development SQL 1") {
		t.Fatalf("expected SQL failure at existing third column, got %v", err)
	}
	for _, column := range []string{"probe_status", "probe_config_hash"} {
		if db.Migrator().HasColumn("llm_configs", column) {
			t.Fatalf("transaction left earlier DDL column %s behind", column)
		}
	}
	var got string
	if err := db.Raw("SELECT version FROM db_versions ORDER BY id DESC LIMIT 1").Scan(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got != config.AppVersion {
		t.Fatalf("development failure changed released version to %q", got)
	}
}

// TestValidateRequiresPayload rejects a registered step that would execute
// nothing, so an empty entry fails at validation instead of silently passing.
func TestValidateRequiresPayload(t *testing.T) {
	err := Validate([]Step{{BaseRelease: config.AppVersion, Revision: 1}})
	if err == nil || !strings.Contains(err.Error(), "neither SQL nor a conversion") {
		t.Fatalf("expected missing-payload error, got %v", err)
	}
}

// TestValidateRequiresDeclaredSQL rejects a registered SQL path without an
// embedded file: declaring a file makes its absence fatal.
func TestValidateRequiresDeclaredSQL(t *testing.T) {
	list := make([]Step, 0, 9)
	for i := 0; i < 8; i++ {
		list = append(list, Step{BaseRelease: config.AppVersion, FromRevision: i, Revision: i + 1,
			Convert: func(*gorm.DB, string) error { return nil }})
	}
	list = append(list, Step{BaseRelease: config.AppVersion, FromRevision: 8, Revision: 9, SQLPath: "scripts/9.sql"})
	err := Validate(list)
	if err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("expected missing SQL error, got %v", err)
	}
}

// TestSourceFilesSkipsAbsentCarriers checks that the source-file mapping only
// includes files the step actually ships, and that a conversion maps to its
// revision-numbered Go file for rewrite detection.
func TestSourceFilesSkipsAbsentCarriers(t *testing.T) {
	sqlOnly := Step{Revision: 2, SQLPath: "scripts/2.sql"}
	if got := sqlOnly.SourceFiles(); len(got) != 1 || got[0] != "server/internal/migration/dev/unreleased/scripts/2.sql" {
		t.Fatalf("SQL-only step source files = %v", got)
	}
	goOnly := Step{Revision: 3, Convert: func(*gorm.DB, string) error { return nil }}
	if got := goOnly.SourceFiles(); len(got) != 1 || got[0] != "server/internal/migration/dev/unreleased/scripts/3.go" {
		t.Fatalf("conversion-only step source files = %v", got)
	}
	both := Step{Revision: 4, SQLPath: "scripts/4.sql", Convert: func(*gorm.DB, string) error { return nil }}
	if got := both.SourceFiles(); len(got) != 2 {
		t.Fatalf("SQL and conversion step source files = %v", got)
	}
}

// TestDevelopmentConversionWithoutSQLCommits verifies a conversion-only step
// runs in the step transaction and receives the staged data root.
func TestDevelopmentConversionWithoutSQLCommits(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "database.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.InitializeFreshAt(db, config.AppVersion); err != nil {
		t.Fatal(err)
	}
	dataRoot := t.TempDir()
	original := registered
	t.Cleanup(func() { registered = original })
	registered = []Step{{
		BaseRelease:  config.AppVersion,
		FromRevision: 0,
		Revision:     1,
		Description:  "conversion only",
		Convert: func(tx *gorm.DB, root string) error {
			if root != dataRoot {
				t.Fatalf("conversion received data root %q, want %q", root, dataRoot)
			}
			return tx.Exec("CREATE TABLE dev_convert_probe (id INTEGER NOT NULL DEFAULT 0)").Error
		},
	}}
	if err := Apply(db, 1, dataRoot); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable("dev_convert_probe") {
		t.Fatal("conversion-only step did not commit its change")
	}
}
