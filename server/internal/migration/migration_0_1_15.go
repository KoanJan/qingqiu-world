package migration

import (
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// migrate_0_1_15 adds the natural-language relation applicability note used by
// Workload-driven Semantic RAG and removes earlier over-structured compatibility
// columns that are no longer part of the minimal closed loop. AutoMigrate
// normally creates the new column before this script runs; the explicit checks
// document the upgrade contract and keep existing databases non-null.
func migrate_0_1_15() {
	if !database.DB.Migrator().HasColumn(&model.KBRelation{}, "applicability_note") {
		if err := database.DB.Migrator().AddColumn(&model.KBRelation{}, "ApplicabilityNote"); err != nil {
			applogger.Error("migration 0.1.15: failed to add relation applicability note", "error", err)
			panic(err)
		}
	}
	if err := database.DB.Model(&model.KBRelation{}).Where("applicability_note IS NULL").Update("applicability_note", "").Error; err != nil {
		applogger.Error("migration 0.1.15: failed to backfill relation applicability note", "error", err)
		panic(err)
	}
	dropDeprecatedColumn_0_1_15("kb_entities", "aliases_json")
	dropDeprecatedColumn_0_1_15("kb_relations", "utility_score")
	applogger.Info("migration 0.1.15: relation schema ready")
}

// dropDeprecatedColumn_0_1_15 removes schema columns for concepts deliberately
// removed in 0.1.15. Failure is fatal because keeping obsolete semantic fields
// would make the database disagree with the domain model.
func dropDeprecatedColumn_0_1_15(table, column string) {
	if !database.DB.Migrator().HasColumn(table, column) {
		return
	}
	statement, ok := dropDeprecatedColumnStatement_0_1_15(table, column)
	if !ok {
		applogger.Error("migration 0.1.15: rejected unknown deprecated relation column", "table", table, "column", column)
		panic("unknown deprecated relation column")
	}
	if err := database.DB.Exec(statement).Error; err != nil {
		applogger.Error("migration 0.1.15: failed to drop deprecated relation column", "table", table, "column", column, "error", err)
		panic(err)
	}
}

// dropDeprecatedColumnStatement_0_1_15 returns fixed DDL for the only columns
// removed by this migration. Keeping the statements enumerated avoids dynamic
// identifier construction in migration SQL.
func dropDeprecatedColumnStatement_0_1_15(table, column string) (string, bool) {
	switch {
	case table == "kb_entities" && column == "aliases_json":
		return "ALTER TABLE kb_entities DROP COLUMN aliases_json", true
	case table == "kb_relations" && column == "utility_score":
		return "ALTER TABLE kb_relations DROP COLUMN utility_score", true
	default:
		return "", false
	}
}
