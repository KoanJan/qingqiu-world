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

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS vectors (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chunk_id INTEGER NOT NULL UNIQUE,
			embedding BLOB NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create vectors table: %w", err)
	}

	return &vectorStore{db: db}, nil
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
