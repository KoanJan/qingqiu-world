package migration

import (
	"encoding/json"

	"gorm.io/gorm"
	applogger "qingqiu-world-server/internal/logger"
)

// inspectLegacyKBBindings preserves warnings for bindings that the 0.1.9 SQL
// script deliberately skips. It runs before that script removes the old column.
func inspectLegacyKBBindings(tx *gorm.DB) {
	var kbIDs []int64
	if err := tx.Table("knowledge_bases").Pluck("id", &kbIDs).Error; err != nil {
		panic(err)
	}
	existing := make(map[int64]bool, len(kbIDs))
	for _, id := range kbIDs {
		existing[id] = true
	}
	var bindings []struct {
		PersonID         int64
		KnowledgeBaseIDs string
	}
	if err := tx.Table("agent_configs").Select("person_id", "knowledge_base_ids").Scan(&bindings).Error; err != nil {
		panic(err)
	}
	for _, binding := range bindings {
		var ids []int64
		if err := json.Unmarshal([]byte(binding.KnowledgeBaseIDs), &ids); err != nil {
			applogger.Warn("migration 0.1.9: corrupted knowledge_base_ids JSON, skipping agent binding",
				"person_id", binding.PersonID, "raw", binding.KnowledgeBaseIDs)
			continue
		}
		for _, id := range ids {
			if !existing[id] {
				applogger.Warn("migration 0.1.9: stale KB binding points to missing knowledge base, skipping",
					"person_id", binding.PersonID, "kb_id", id)
			}
		}
	}
}
