package migration

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// Test014BackfillWithHistoricalColumns catches accidental use of runtime models:
// the source tables contain only fields available when 0.1.14 was released.
func Test014BackfillWithHistoricalColumns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "historical.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{
		`CREATE TABLE documents (id INTEGER PRIMARY KEY, title TEXT NOT NULL, file_path TEXT NOT NULL, status INTEGER NOT NULL)`,
		`CREATE TABLE document_chunks (id INTEGER PRIMARY KEY, document_id INTEGER NOT NULL, chunk_index INTEGER NOT NULL, content TEXT NOT NULL, deleted INTEGER NOT NULL)`,
		`CREATE TABLE knowledge_bases (id INTEGER PRIMARY KEY)`,
	} {
		if err := db.Exec(schema).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec(`INSERT INTO documents (id, title, file_path, status) VALUES (1, 'Old title', 'old.pdf', 2)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO document_chunks (id, document_id, chunk_index, content, deleted) VALUES (3, 1, 0, 'Old body', 0)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := applyReleaseSQL(tx, "0.1.14"); err != nil {
			return err
		}
		migrate_0_1_14(tx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var revisionID int64
	if err := db.Raw(`SELECT active_revision_id FROM documents WHERE id = 1`).Scan(&revisionID).Error; err != nil || revisionID == 0 {
		t.Fatalf("active revision = %d, error = %v", revisionID, err)
	}
	var nodes, mappings int64
	if err := db.Raw(`SELECT COUNT(*) FROM content_nodes WHERE revision_id = ?`, revisionID).Scan(&nodes).Error; err != nil || nodes != 2 {
		t.Fatalf("content nodes = %d, error = %v", nodes, err)
	}
	if err := db.Raw(`SELECT COUNT(*) FROM document_chunk_nodes WHERE chunk_id = 3`).Scan(&mappings).Error; err != nil || mappings != 1 {
		t.Fatalf("chunk mappings = %d, error = %v", mappings, err)
	}
}

func Test011SeedWithHistoricalColumns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "historical.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE persons (id INTEGER PRIMARY KEY, type INTEGER NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO persons (id, type) VALUES (1, 1), (2, 2)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return applyReleaseSQL(tx, "0.1.1")
	}); err != nil {
		t.Fatal(err)
	}
	var personID int64
	if err := db.Raw(`SELECT person_id FROM agent_states`).Scan(&personID).Error; err != nil || personID != 1 {
		t.Fatalf("seeded person = %d, error = %v", personID, err)
	}
}
