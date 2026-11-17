package migration

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	applogger "qingqiu-world-server/internal/logger"

	"gorm.io/gorm"
)

const legacyCanonicalVersion = "legacy-canonical-v1"

// These fields are fixed to the schema at 0.1.14. Runtime models may gain
// columns in later releases and must not be used while upgrading through it.
type legacyDocument_0_1_14 struct {
	ID               int64
	Title            string
	FilePath         string
	ActiveRevisionID int64
}

type legacyChunk_0_1_14 struct {
	ID         int64
	ChunkIndex int
	Content    string
}

// migrate_0_1_14 maps existing ready documents to immutable legacy revisions.
// It does not call an embedding provider or reparse source files, so upgrades
// remain offline-safe. Existing chunks become paragraph ContentNodes and
// continue serving retrieval until users explicitly rebuild representations.
func migrate_0_1_14(tx *gorm.DB) {
	var documents []legacyDocument_0_1_14
	if err := tx.Raw(`SELECT id, title, file_path, active_revision_id FROM documents WHERE status = 2`).Scan(&documents).Error; err != nil {
		applogger.Error("migration 0.1.14: failed to load ready documents", "error", err)
		panic(err)
	}

	migrated := 0
	skipped := 0
	for _, doc := range documents {
		if doc.ActiveRevisionID != 0 {
			continue
		}

		var chunks []legacyChunk_0_1_14
		if err := tx.Raw(`SELECT id, chunk_index, content FROM document_chunks
			WHERE document_id = ? AND deleted = 0 ORDER BY chunk_index ASC, id ASC`, doc.ID).Scan(&chunks).Error; err != nil {
			abortMigration_0_1_14("load document chunks", doc.ID, err)
		}
		if len(chunks) == 0 {
			applogger.Warn("migration 0.1.14: ready document has no active chunks; reimport required", "doc_id", doc.ID)
			skipped++
			continue
		}

		fingerprint := legacyRevisionFingerprint(doc, chunks)
		var revisionID int64
		lookup := tx.Raw(`SELECT id FROM document_revisions WHERE document_id = ? AND canonical_fingerprint = ?`, doc.ID, fingerprint).Scan(&revisionID)
		if lookup.Error != nil {
			abortMigration_0_1_14("lookup legacy revision", doc.ID, lookup.Error)
		}
		if lookup.RowsAffected == 0 {
			now := time.Now()
			if err := tx.Raw(`INSERT INTO document_revisions
				(document_id, content_hash, parser_id, parser_version, normalizer_version,
				canonical_fingerprint, blob_path, status, created_at, updated_at)
				VALUES (?, ?, 'legacy-chunk', ?, ?, ?, ?, 2, ?, ?) RETURNING id`,
				doc.ID, legacyContentHash(chunks), legacyCanonicalVersion, legacyCanonicalVersion,
				fingerprint, doc.FilePath, now, now).Scan(&revisionID).Error; err != nil {
				abortMigration_0_1_14("create legacy revision", doc.ID, err)
			}
		}

		if err := tx.Exec(`DELETE FROM content_nodes WHERE revision_id = ?`, revisionID).Error; err != nil {
			abortMigration_0_1_14("clear legacy content nodes", doc.ID, err)
		}
		if err := tx.Exec(`DELETE FROM document_chunk_nodes WHERE chunk_id IN ?`, chunkIDs(chunks)).Error; err != nil {
			abortMigration_0_1_14("clear legacy chunk-node mappings", doc.ID, err)
		}

		var rootID int64
		if err := tx.Raw(`INSERT INTO content_nodes
			(document_id, revision_id, parent_id, ordinal, node_type, text, content_hash,
			locator_json, metadata_json, created_at)
			VALUES (?, ?, 0, 0, 0, ?, ?, '{}', '{"capability":"legacy-chunk"}', ?) RETURNING id`,
			doc.ID, revisionID, doc.Title, hashText(doc.Title), time.Now()).Scan(&rootID).Error; err != nil {
			abortMigration_0_1_14("create legacy root node", doc.ID, err)
		}

		for ordinal, chunk := range chunks {
			searchText := chunk.Content
			displayText := chunk.Content
			inputFingerprint := legacyUnitFingerprint(revisionID, chunk.ChunkIndex, searchText)
			if err := tx.Exec(`UPDATE document_chunks SET revision_id = ?, search_text = ?,
				display_text = ?, token_count = ?, input_fingerprint = ? WHERE id = ?`,
				revisionID, searchText, displayText, legacyTokenCount(searchText), inputFingerprint, chunk.ID).Error; err != nil {
				abortMigration_0_1_14("backfill retrieval unit", doc.ID, err)
			}

			var nodeID int64
			if err := tx.Raw(`INSERT INTO content_nodes
				(document_id, revision_id, parent_id, ordinal, node_type, text, content_hash,
				locator_json, metadata_json, created_at)
				VALUES (?, ?, ?, ?, 1, ?, ?, '{}', '{"capability":"legacy-chunk"}', ?) RETURNING id`,
				doc.ID, revisionID, rootID, ordinal+1, displayText, hashText(displayText), time.Now()).Scan(&nodeID).Error; err != nil {
				abortMigration_0_1_14("create legacy content node", doc.ID, err)
			}
			if err := tx.Exec(`INSERT INTO document_chunk_nodes (chunk_id, node_id, ordinal)
				VALUES (?, ?, 0)`, chunk.ID, nodeID).Error; err != nil {
				abortMigration_0_1_14("create legacy chunk-node mapping", doc.ID, err)
			}
		}

		if err := tx.Exec(`UPDATE documents SET source_kind = 0, source_uri = ?, active_revision_id = ? WHERE id = ?`,
			fmt.Sprintf("upload://%d", doc.ID), revisionID, doc.ID).Error; err != nil {
			abortMigration_0_1_14("activate legacy revision", doc.ID, err)
		}
		migrated++
	}

	applogger.Info("migration 0.1.14: created legacy canonical revisions", "migrated", migrated, "skipped", skipped)
}

// abortMigration_0_1_14 leaves rollback to the enclosing release transaction.
func abortMigration_0_1_14(operation string, docID int64, err error) {
	applogger.Error("migration 0.1.14: failed", "operation", operation, "doc_id", docID, "error", err)
	panic(err)
}

func legacyRevisionFingerprint(doc legacyDocument_0_1_14, chunks []legacyChunk_0_1_14) string {
	return hashText(fmt.Sprintf("%s|%d|%s", legacyCanonicalVersion, doc.ID, legacyContentHash(chunks)))
}

func legacyContentHash(chunks []legacyChunk_0_1_14) string {
	parts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		parts = append(parts, chunk.Content)
	}
	return hashText(strings.Join(parts, "\n"))
}

func legacyUnitFingerprint(revisionID int64, chunkIndex int, searchText string) string {
	return hashText(fmt.Sprintf("legacy-unit-v1|%d|%d|%s", revisionID, chunkIndex, searchText))
}

func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:v1:%x", sum[:])
}

func legacyTokenCount(value string) int {
	count := len(strings.Fields(value))
	if count == 0 && value != "" {
		return len([]rune(value))
	}
	return count
}

func chunkIDs(chunks []legacyChunk_0_1_14) []int64 {
	ids := make([]int64, 0, len(chunks))
	for _, chunk := range chunks {
		ids = append(ids, chunk.ID)
	}
	return ids
}
