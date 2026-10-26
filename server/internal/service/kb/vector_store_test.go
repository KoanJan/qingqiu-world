package kb

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestNewVectorStoreRepairsNullableCreatedAt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vectors.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open legacy vector store: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE vectors (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chunk_id INTEGER NOT NULL UNIQUE,
			embedding BLOB NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		t.Fatalf("create legacy vectors table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO vectors (chunk_id, embedding) VALUES (?, ?)`, int64(7), []byte("embedding")); err != nil {
		t.Fatalf("insert legacy vector: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy vector store: %v", err)
	}

	store, err := newVectorStore(dbPath)
	if err != nil {
		t.Fatalf("open repaired vector store: %v", err)
	}
	defer store.Close()

	notNull, err := vectorCreatedAtIsNotNull(store.db)
	if err != nil {
		t.Fatalf("inspect repaired vectors table: %v", err)
	}
	if !notNull {
		t.Fatal("created_at should be NOT NULL after schema repair")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM vectors WHERE chunk_id = ? AND created_at IS NOT NULL`, int64(7)).Scan(&count); err != nil {
		t.Fatalf("count repaired vector rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("legacy vector row was not preserved: count=%d", count)
	}
}
