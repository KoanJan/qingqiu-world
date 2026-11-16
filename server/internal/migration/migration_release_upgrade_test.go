package migration

import (
	"os"
	"testing"

	"qingqiu-world-server/internal/config"
)

// TestUpgradeFrom010Baseline runs every release step against the schema from
// the 0.1.0 tag, including data migrations for an existing agent and document.
func TestUpgradeFrom010Baseline(t *testing.T) {
	db := startupTestDB(t)
	schema, err := os.ReadFile("testdata/0.1.0.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(schema)).Error; err != nil {
		t.Fatalf("create 0.1.0 schema: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO db_versions (version, description, applied_at) VALUES ('0.1.0', 'baseline', CURRENT_TIMESTAMP)`,
		`INSERT INTO persons (id, name, type, created_at, updated_at) VALUES (1, 'historical-agent', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO knowledge_bases (id, name, created_at, updated_at) VALUES (1, 'historical-kb', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO documents (id, knowledge_base_id, title, file_path, chunk_count, status, created_at, updated_at)
		 VALUES (1, 1, 'Historical document', 'document.txt', 1, 2, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		`INSERT INTO document_chunks (id, knowledge_base_id, document_id, chunk_index, content, created_at)
		 VALUES (1, 1, 1, 0, 'Historical content', CURRENT_TIMESTAMP)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("seed 0.1.0 data: %v", err)
		}
	}
	if db.Migrator().HasColumn("documents", "active_revision_id") {
		t.Fatal("baseline fixture already contains the 0.1.14 document column")
	}

	Run()
	Run()
	if got := getDBVersion(); got != config.AppVersion {
		t.Fatalf("database version = %s, want %s", got, config.AppVersion)
	}
	var currentVersionRows int64
	if err := db.Raw(`SELECT COUNT(*) FROM db_versions WHERE version = ?`, config.AppVersion).Scan(&currentVersionRows).Error; err != nil || currentVersionRows != 1 {
		t.Fatalf("current version rows = %d, error = %v", currentVersionRows, err)
	}
	var energy int
	if err := db.Raw(`SELECT energy FROM agent_states WHERE person_id = 1`).Scan(&energy).Error; err != nil || energy != 100 {
		t.Fatalf("seeded agent energy = %d, error = %v", energy, err)
	}
	var revisionID, mappedNodes int64
	if err := db.Raw(`SELECT active_revision_id FROM documents WHERE id = 1`).Scan(&revisionID).Error; err != nil || revisionID == 0 {
		t.Fatalf("active document revision = %d, error = %v", revisionID, err)
	}
	if err := db.Raw(`SELECT COUNT(*) FROM document_chunk_nodes WHERE chunk_id = 1`).Scan(&mappedNodes).Error; err != nil || mappedNodes != 1 {
		t.Fatalf("historical chunk mappings = %d, error = %v", mappedNodes, err)
	}
}
