package kb

import (
	"database/sql"
	"fmt"
	"math"

	"qingqiu-world-server/internal/service/vectorutils"

	_ "github.com/glebarez/go-sqlite/compat"
)

// vectorStore manages vector persistence in a per-KB SQLite file.
// Each knowledge base has its own vectors.db containing a single vectors table.
type vectorStore struct {
	db *sql.DB
}

// newVectorStore opens or creates a vector store at the given SQLite file path.
func newVectorStore(dbPath string) (*vectorStore, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open vector store: %w", err)
	}

	if err := ensureVectorTableSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	return &vectorStore{db: db}, nil
}

// ensureVectorTableSchema creates or repairs the per-KB vector table. Older
// vector stores allowed nullable created_at; this keeps existing embeddings
// while making the schema conform to the project's non-null field rule.
func ensureVectorTableSchema(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS vectors (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chunk_id INTEGER NOT NULL UNIQUE,
			embedding BLOB NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("failed to create vectors table: %w", err)
	}

	ok, err := vectorCreatedAtIsNotNull(db)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return rebuildVectorTableWithNonNullCreatedAt(db)
}

// vectorCreatedAtIsNotNull inspects SQLite table metadata instead of assuming
// CREATE TABLE IF NOT EXISTS upgraded an existing vectors table.
func vectorCreatedAtIsNotNull(db *sql.DB) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(vectors)`)
	if err != nil {
		return false, fmt.Errorf("inspect vectors table schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return false, fmt.Errorf("scan vectors schema column: %w", err)
		}
		if name == "created_at" {
			return notNull == 1, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate vectors schema: %w", err)
	}
	return false, nil
}

// rebuildVectorTableWithNonNullCreatedAt performs a bounded in-place SQLite
// migration for the small per-KB vector table schema. It preserves IDs,
// chunk IDs and embeddings, and fills missing timestamps deterministically.
func rebuildVectorTableWithNonNullCreatedAt(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin vector table schema repair: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	statements := []string{
		`ALTER TABLE vectors RENAME TO vectors_legacy_nullable_created_at`,
		`CREATE TABLE vectors (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chunk_id INTEGER NOT NULL UNIQUE,
			embedding BLOB NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT INTO vectors (id, chunk_id, embedding, created_at)
			SELECT id, chunk_id, embedding, COALESCE(created_at, CURRENT_TIMESTAMP)
			FROM vectors_legacy_nullable_created_at`,
		`DROP TABLE vectors_legacy_nullable_created_at`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("repair vectors table schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit vector table schema repair: %w", err)
	}
	committed = true
	return nil
}

// Insert adds a vector embedding for a chunk.
func (vs *vectorStore) Insert(chunkID int64, embedding []float32) error {
	blob := vectorutils.Float32SliceToBlob(embedding)
	_, err := vs.db.Exec(
		"INSERT OR REPLACE INTO vectors (chunk_id, embedding) VALUES (?, ?)",
		chunkID, blob,
	)
	return err
}

// InsertBatch adds multiple vectors in a transaction.
func (vs *vectorStore) InsertBatch(entries []vectorEntry) error {
	tx, err := vs.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT OR REPLACE INTO vectors (chunk_id, embedding) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for i, e := range entries {
		if !isValidvectorEntry(e) {
			return fmt.Errorf("invalid embedding for chunk_id %d at index %d: empty or contains NaN/Inf", e.ChunkID, i)
		}
		blob := vectorutils.Float32SliceToBlob(e.Embedding)
		if _, err := stmt.Exec(e.ChunkID, blob); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// isValidvectorEntry checks if a vector entry has a valid embedding.
func isValidvectorEntry(e vectorEntry) bool {
	if len(e.Embedding) == 0 {
		return false
	}
	for _, v := range e.Embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return false
		}
	}
	return true
}

// Get retrieves the embedding for a chunk.
func (vs *vectorStore) Get(chunkID int64) ([]float32, error) {
	var blob []byte
	err := vs.db.QueryRow("SELECT embedding FROM vectors WHERE chunk_id = ?", chunkID).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return vectorutils.BlobToFloat32Slice(blob), nil
}

// GetAll retrieves all vectors from the store.
func (vs *vectorStore) GetAll() ([]vectorEntry, error) {
	rows, err := vs.db.Query("SELECT chunk_id, embedding FROM vectors")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []vectorEntry
	for rows.Next() {
		var chunkID int64
		var blob []byte
		if err := rows.Scan(&chunkID, &blob); err != nil {
			return nil, err
		}
		entries = append(entries, vectorEntry{
			ChunkID:   chunkID,
			Embedding: vectorutils.BlobToFloat32Slice(blob),
		})
	}
	return entries, rows.Err()
}

// Count returns the total number of vectors.
func (vs *vectorStore) Count() (int, error) {
	var count int
	err := vs.db.QueryRow("SELECT COUNT(*) FROM vectors").Scan(&count)
	return count, err
}

// Delete removes vectors for the given chunk IDs.
func (vs *vectorStore) Delete(chunkIDs []int64) error {
	if len(chunkIDs) == 0 {
		return nil
	}
	tx, err := vs.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("DELETE FROM vectors WHERE chunk_id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, id := range chunkIDs {
		if _, err := stmt.Exec(id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Close closes the underlying database connection.
func (vs *vectorStore) Close() {
	vs.db.Close()
}

// vectorEntry represents a chunk vector record.
type vectorEntry struct {
	ChunkID   int64
	Embedding []float32
}
