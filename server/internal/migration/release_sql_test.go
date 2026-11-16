package migration

import (
	"errors"
	"testing"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

func TestReleaseStepRollsBackSchemaDataAndVersion(t *testing.T) {
	db := startupTestDB(t)
	if err := db.Exec("CREATE TABLE jinshus (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().CreateTable(&model.DBVersion{}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("stop after data write")
	step := migration{version: "0.1.8", fn: func(tx *gorm.DB) {
		if err := tx.Exec("INSERT INTO jinshus (id) VALUES (1)").Error; err != nil {
			panic(err)
		}
		panic(want)
	}}
	func() {
		defer func() {
			if got := recover(); got != want {
				t.Fatalf("panic = %v, want %v", got, want)
			}
		}()
		_ = executeReleaseStep(step)
	}()
	if db.Migrator().HasColumn("jinshus", "is_read") {
		t.Fatal("schema change survived rollback")
	}
	var rows int64
	if err := db.Table("jinshus").Count(&rows).Error; err != nil || rows != 0 {
		t.Fatalf("data rows after rollback = %d, error = %v", rows, err)
	}
	if err := db.Model(&model.DBVersion{}).Count(&rows).Error; err != nil || rows != 0 {
		t.Fatalf("version rows after rollback = %d, error = %v", rows, err)
	}
	// The same step must succeed from the original schema after the failure.
	step.fn = nil
	if err := executeReleaseStep(step); err != nil {
		t.Fatal(err)
	}
	if !database.DB.Migrator().HasColumn("jinshus", "is_read") {
		t.Fatal("retry did not apply schema")
	}
}

func TestSQLiteRollsBackEarlierStatementsInFailedScript(t *testing.T) {
	db := startupTestDB(t)
	err := db.Transaction(func(tx *gorm.DB) error {
		return tx.Exec("CREATE TABLE partial_schema (id INTEGER); CREATE INDEX invalid_index ON missing_table(id);").Error
	})
	if err == nil {
		t.Fatal("expected the second SQL statement to fail")
	}
	if db.Migrator().HasTable("partial_schema") {
		t.Fatal("the earlier CREATE TABLE survived the failed script")
	}
}

func TestReleaseSQL_0_1_9ConvertsValidKnowledgeBaseBindings(t *testing.T) {
	db := startupTestDB(t)
	for _, sql := range []string{
		"CREATE TABLE agent_configs (id INTEGER PRIMARY KEY, person_id INTEGER, llm_config_id INTEGER, knowledge_base_ids TEXT)",
		"CREATE UNIQUE INDEX idx_agent_configs_person_id ON agent_configs(person_id)",
		"CREATE INDEX idx_agent_configs_llm_config_id ON agent_configs(llm_config_id)",
		"CREATE TABLE knowledge_bases (id INTEGER PRIMARY KEY)",
		"INSERT INTO knowledge_bases (id) VALUES (3)",
		`INSERT INTO agent_configs (id, person_id, knowledge_base_ids) VALUES (1, 10, '[3,99]'), (2, 11, 'broken'), (3, 12, '["3"]')`,
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return applyReleaseSQL(tx, "0.1.9") }); err != nil {
		t.Fatal(err)
	}
	var grants []model.KBAccess
	if err := db.Find(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].PersonID != 10 || grants[0].KBID != 3 {
		t.Fatalf("converted grants = %+v", grants)
	}
	if db.Migrator().HasColumn("agent_configs", "knowledge_base_ids") {
		t.Fatal("legacy binding column survived")
	}
	for _, index := range []string{"idx_agent_configs_person_id", "idx_agent_configs_llm_config_id"} {
		var count int64
		if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?", index).Scan(&count).Error; err != nil || count != 1 {
			t.Fatalf("index %s missing: count=%d error=%v", index, count, err)
		}
	}
}
