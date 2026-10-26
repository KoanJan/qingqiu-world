package migration

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"

	"gorm.io/gorm"
)

const legacyCanonicalVersion = "legacy-canonical-v1"

// migrate_0_1_14 maps existing ready documents to immutable legacy revisions.
// It does not call an embedding provider or reparse source files, so upgrades
// remain offline-safe. Existing chunks become paragraph ContentNodes and
// continue serving retrieval until users explicitly rebuild representations.
func migrate_0_1_14() {
	var documents []model.Document
	if err := database.DB.Where("status = ?", model.DocumentStatusReady).Find(&documents).Error; err != nil {
		applogger.Error("migration 0.1.14: failed to load ready documents", "error", err)
		panic(err)
	}

	tx := database.DB.Begin()
	if tx.Error != nil {
		applogger.Error("migration 0.1.14: failed to begin transaction", "error", tx.Error)
		panic(tx.Error)
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()

	migrated := 0
	skipped := 0
	for _, doc := range documents {
		if doc.ActiveRevisionID != 0 {
			continue
		}

		var chunks []model.DocumentChunk
		if err := tx.Where("document_id = ? AND deleted = 0", doc.ID).Order("chunk_index ASC, id ASC").Find(&chunks).Error; err != nil {
			rollbackMigration_0_1_14(tx, "load document chunks", doc.ID, err)
		}
		if len(chunks) == 0 {
			applogger.Warn("migration 0.1.14: ready document has no active chunks; reimport required", "doc_id", doc.ID)
			skipped++
			continue
		}

		fingerprint := legacyRevisionFingerprint(doc, chunks)
		revision := model.DocumentRevision{}
		err := tx.Where("document_id = ? AND canonical_fingerprint = ?", doc.ID, fingerprint).First(&revision).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			rollbackMigration_0_1_14(tx, "lookup legacy revision", doc.ID, err)
		}
		if err == gorm.ErrRecordNotFound {
			revision = model.DocumentRevision{
				DocumentID:           doc.ID,
				ContentHash:          legacyContentHash(chunks),
				ParserID:             "legacy-chunk",
				ParserVersion:        legacyCanonicalVersion,
				NormalizerVersion:    legacyCanonicalVersion,
				CanonicalFingerprint: fingerprint,
				BlobPath:             doc.FilePath,
				Status:               model.DocumentRevisionStatusReady,
			}
			if err := tx.Create(&revision).Error; err != nil {
				rollbackMigration_0_1_14(tx, "create legacy revision", doc.ID, err)
			}
		}

		if err := tx.Where("revision_id = ?", revision.ID).Delete(&model.ContentNode{}).Error; err != nil {
			rollbackMigration_0_1_14(tx, "clear legacy content nodes", doc.ID, err)
		}
		if err := tx.Where("chunk_id IN ?", chunkIDs(chunks)).Delete(&model.DocumentChunkNode{}).Error; err != nil {
			rollbackMigration_0_1_14(tx, "clear legacy chunk-node mappings", doc.ID, err)
		}

		root := model.ContentNode{
			DocumentID:   doc.ID,
			RevisionID:   revision.ID,
			ParentID:     0,
			Ordinal:      0,
			NodeType:     model.ContentNodeTypeDocument,
			Text:         doc.Title,
			ContentHash:  hashText(doc.Title),
			LocatorJSON:  "{}",
			MetadataJSON: `{"capability":"legacy-chunk"}`,
		}
		if err := tx.Create(&root).Error; err != nil {
			rollbackMigration_0_1_14(tx, "create legacy root node", doc.ID, err)
		}

		for ordinal, chunk := range chunks {
			searchText := chunk.Content
			displayText := chunk.Content
			inputFingerprint := legacyUnitFingerprint(revision.ID, chunk.ChunkIndex, searchText)
			if err := tx.Model(&model.DocumentChunk{}).Where("id = ?", chunk.ID).Updates(map[string]interface{}{
				"revision_id":       revision.ID,
				"search_text":       searchText,
				"display_text":      displayText,
				"token_count":       legacyTokenCount(searchText),
				"input_fingerprint": inputFingerprint,
			}).Error; err != nil {
				rollbackMigration_0_1_14(tx, "backfill retrieval unit", doc.ID, err)
			}

			node := model.ContentNode{
				DocumentID:   doc.ID,
				RevisionID:   revision.ID,
				ParentID:     root.ID,
				Ordinal:      ordinal + 1,
				NodeType:     model.ContentNodeTypeParagraph,
				Text:         displayText,
				ContentHash:  hashText(displayText),
				LocatorJSON:  "{}",
				MetadataJSON: `{"capability":"legacy-chunk"}`,
			}
			if err := tx.Create(&node).Error; err != nil {
				rollbackMigration_0_1_14(tx, "create legacy content node", doc.ID, err)
			}
			if err := tx.Create(&model.DocumentChunkNode{ChunkID: chunk.ID, NodeID: node.ID, Ordinal: 0}).Error; err != nil {
				rollbackMigration_0_1_14(tx, "create legacy chunk-node mapping", doc.ID, err)
			}
		}

		if err := tx.Model(&model.Document{}).Where("id = ?", doc.ID).Updates(map[string]interface{}{
			"source_kind":        model.DocumentSourceKindLocalUpload,
			"source_uri":         fmt.Sprintf("upload://%d", doc.ID),
			"active_revision_id": revision.ID,
		}).Error; err != nil {
			rollbackMigration_0_1_14(tx, "activate legacy revision", doc.ID, err)
		}
		migrated++
	}

	if err := tx.Commit().Error; err != nil {
		applogger.Error("migration 0.1.14: failed to commit", "error", err)
		panic(err)
	}
	applogger.Info("migration 0.1.14: created legacy canonical revisions", "migrated", migrated, "skipped", skipped)
}

// rollbackMigration_0_1_14 aborts the transaction because partial canonical
// migrations must not advance the database version.
func rollbackMigration_0_1_14(tx *gorm.DB, operation string, docID int64, err error) {
	applogger.Error("migration 0.1.14: failed", "operation", operation, "doc_id", docID, "error", err)
	tx.Rollback()
	panic(err)
}

func legacyRevisionFingerprint(doc model.Document, chunks []model.DocumentChunk) string {
	return hashText(fmt.Sprintf("%s|%d|%s", legacyCanonicalVersion, doc.ID, legacyContentHash(chunks)))
}

func legacyContentHash(chunks []model.DocumentChunk) string {
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

func chunkIDs(chunks []model.DocumentChunk) []int64 {
	ids := make([]int64, 0, len(chunks))
	for _, chunk := range chunks {
		ids = append(ids, chunk.ID)
	}
	return ids
}
