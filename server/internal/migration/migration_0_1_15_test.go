package migration

import (
	"path/filepath"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestMigrate_0_1_15RemovesContractedRelationColumns verifies the 0.1.15
// schema matches the contracted semantic model: applicability_note remains,
// while alias and utility-score compatibility columns are removed.
func TestMigrate_0_1_15RemovesContractedRelationColumns(t *testing.T) {
	oldDB := database.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migration-0.1.15.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open migration test database: %v", err)
	}
	database.DB = db
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		database.DB = oldDB
	})

	if err := db.AutoMigrate(&model.KBEntity{}, &model.KBRelation{}); err != nil {
		t.Fatalf("auto-migrate relation schema: %v", err)
	}
	if err := db.Exec("ALTER TABLE kb_entities ADD COLUMN aliases_json text NOT NULL DEFAULT '[]'").Error; err != nil {
		t.Fatalf("add legacy aliases column: %v", err)
	}
	if err := db.Exec("ALTER TABLE kb_relations ADD COLUMN utility_score integer NOT NULL DEFAULT 0").Error; err != nil {
		t.Fatalf("add legacy utility column: %v", err)
	}

	migrate_0_1_15()

	if db.Migrator().HasColumn("kb_entities", "aliases_json") {
		t.Fatal("aliases_json should be removed from kb_entities")
	}
	if db.Migrator().HasColumn("kb_relations", "utility_score") {
		t.Fatal("utility_score should be removed from kb_relations")
	}
	if !db.Migrator().HasColumn("kb_relations", "applicability_note") {
		t.Fatal("applicability_note should remain on kb_relations")
	}
}
