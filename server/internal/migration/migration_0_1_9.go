package migration

import (
	"encoding/json"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm/clause"
)

// legacyKBBinding mirrors a row of agent_configs during the 0.1.9 migration,
// exposing only the columns we need to read.
type legacyKBBinding struct {
	PersonID         int64
	KnowledgeBaseIDs string
}

// migrate_0_1_9 decouples knowledge bases from agents (0.1.9 "library" model):
// legacy per-agent KB bindings stored in agent_configs.knowledge_base_ids
// (a JSON array of KB IDs) are converted into kb_access grant rows, and the
// legacy column is dropped.
//
// GORM AutoMigrate already created the empty kb_access table and never drops
// columns, so the legacy data is still readable here. Bindings pointing to
// knowledge bases that no longer exist are skipped with a warning. The
// column-existence guard makes re-runs a no-op.
func migrate_0_1_9() {
	migrator := database.DB.Migrator()
	if !migrator.HasColumn(&model.AgentConfig{}, "knowledge_base_ids") {
		return
	}

	// Load existing KB IDs so stale bindings can be filtered out.
	var kbIDs []int64
	if err := database.DB.Model(&model.KnowledgeBase{}).Pluck("id", &kbIDs).Error; err != nil {
		applogger.Error("migration 0.1.9: failed to list knowledge base IDs", "error", err)
		panic(err)
	}
	existingKBs := make(map[int64]struct{}, len(kbIDs))
	for _, id := range kbIDs {
		existingKBs[id] = struct{}{}
	}

	var bindings []legacyKBBinding
	if err := database.DB.Model(&model.AgentConfig{}).
		Select("person_id", "knowledge_base_ids").
		Scan(&bindings).Error; err != nil {
		applogger.Error("migration 0.1.9: failed to read legacy KB bindings", "error", err)
		panic(err)
	}

	converted := 0
	skipped := 0
	for _, b := range bindings {
		var ids []int64
		if err := json.Unmarshal([]byte(b.KnowledgeBaseIDs), &ids); err != nil {
			// Corrupted JSON must not fail the whole migration: log loudly and
			// continue with the next agent (dev-stage data, best effort).
			applogger.Warn("migration 0.1.9: corrupted knowledge_base_ids JSON, skipping agent binding",
				"person_id", b.PersonID, "raw", b.KnowledgeBaseIDs)
			skipped++
			continue
		}
		for _, kbID := range ids {
			if _, ok := existingKBs[kbID]; !ok {
				applogger.Warn("migration 0.1.9: stale KB binding points to missing knowledge base, skipping",
					"person_id", b.PersonID, "kb_id", kbID)
				skipped++
				continue
			}
			grant := model.KBAccess{PersonID: b.PersonID, KBID: kbID}
			// Idempotent insert: a re-run after a mid-migration crash (version
			// not yet recorded) replays grants already converted before the
			// failure; the composite unique index plus DoNothing absorbs them.
			if err := database.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&grant).Error; err != nil {
				applogger.Error("migration 0.1.9: failed to create kb_access grant",
					"person_id", b.PersonID, "kb_id", kbID, "error", err)
				panic(err)
			}
			converted++
		}
	}

	if err := migrator.DropColumn(&model.AgentConfig{}, "knowledge_base_ids"); err != nil {
		applogger.Error("migration 0.1.9: failed to drop agent_configs.knowledge_base_ids column", "error", err)
		panic(err)
	}

	applogger.Info("migration 0.1.9: converted legacy agent KB bindings into kb_access grants",
		"converted", converted, "skipped", skipped)
}
