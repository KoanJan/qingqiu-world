package kb

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
)

// recoverEmbeddingGenerations schedules an automatic rebuild when stored
// vectors cannot be proven compatible with the configured embedding model.
// Existing vectors remain readable until a complete replacement is verified.
func recoverEmbeddingGenerations() {
	cfg := dops.GetEmbeddingConfig()
	if cfg == nil {
		applogger.Warn("KB embedding generation check skipped: no embedding config")
		return
	}
	fingerprint := embeddingFingerprint(cfg)
	var knowledgeBases []model.KnowledgeBase
	if err := database.DB.Find(&knowledgeBases).Error; err != nil {
		applogger.Error("KB embedding generation check failed", "error", err)
		return
	}
	for _, knowledgeBase := range knowledgeBases {
		if knowledgeBase.VectorCount == 0 || knowledgeBase.EmbeddingFingerprint == fingerprint {
			continue
		}
		if err := database.DB.Model(&model.KnowledgeBase{}).Where("id = ?", knowledgeBase.ID).Updates(map[string]interface{}{
			"embedding_rebuild_status": model.KnowledgeBaseEmbeddingRebuildRunning,
			"embedding_rebuild_error":  "",
		}).Error; err != nil {
			applogger.Error("KB embedding rebuild could not be scheduled", "kb_id", knowledgeBase.ID, "error", err)
			continue
		}
		go rebuildEmbeddingGeneration(context.Background(), knowledgeBase.ID, cfg, fingerprint)
	}
}

func embeddingFingerprint(cfg *model.EmbeddingConfig) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("embedding-v1|%s|%s|%d", cfg.BaseURL, cfg.ModelID, embeddingDim)))
	return fmt.Sprintf("sha256:v1:%x", sum[:])
}

// rebuildEmbeddingGeneration writes a complete temporary vector database and
// swaps it only after every active retrieval unit was embedded successfully.
func rebuildEmbeddingGeneration(ctx context.Context, kbID int64, cfg *model.EmbeddingConfig, fingerprint string) {
	var chunks []model.DocumentChunk
	if err := database.DB.Table("document_chunks AS chunks").
		Joins("JOIN documents AS documents ON documents.id = chunks.document_id").
		Where("chunks.knowledge_base_id = ? AND chunks.deleted = 0 AND documents.status = ? AND chunks.revision_id = documents.active_revision_id", kbID, model.DocumentStatusReady).
		Order("chunks.id ASC").Find(&chunks).Error; err != nil {
		finishEmbeddingRebuild(kbID, fingerprint, 0, err)
		return
	}
	service := llm.NewEmbeddingService(cfg.BaseURL, cfg.APIKey, cfg.ModelID, embeddingDim)
	tmpPath := filepath.Join(config.Get().GetKBDir(), fmt.Sprintf("%d", kbID), "vectors.db.rebuild")
	if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
		finishEmbeddingRebuild(kbID, fingerprint, 0, fmt.Errorf("remove stale temporary vector store: %w", err))
		return
	}
	store, err := newVectorStore(tmpPath)
	if err != nil {
		finishEmbeddingRebuild(kbID, fingerprint, 0, err)
		return
	}
	defer store.Close()
	for start := 0; start < len(chunks); start += embedBatchSize {
		end := start + embedBatchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		texts := make([]string, end-start)
		for i := start; i < end; i++ {
			texts[i-start] = chunks[i].SearchText
		}
		vectors, err := service.Embed(ctx, texts)
		if err != nil || len(vectors) != len(texts) {
			if err == nil {
				err = fmt.Errorf("embedding response count %d does not match request %d", len(vectors), len(texts))
			}
			finishEmbeddingRebuild(kbID, fingerprint, 0, err)
			return
		}
		entries := make([]vectorEntry, len(vectors))
		for i := range vectors {
			entries[i] = vectorEntry{ChunkID: chunks[start+i].ID, Embedding: vectors[i]}
		}
		if err := store.InsertBatch(entries); err != nil {
			finishEmbeddingRebuild(kbID, fingerprint, 0, err)
			return
		}
	}
	store.Close()
	if err := swapRebuiltVectorStore(kbID, tmpPath); err != nil {
		finishEmbeddingRebuild(kbID, fingerprint, 0, err)
		return
	}
	finishEmbeddingRebuild(kbID, fingerprint, len(chunks), nil)
}

func swapRebuiltVectorStore(kbID int64, temporaryPath string) error {
	// Block lazy manager creation while the backing file changes. Existing
	// readers retain their already-open SQLite handle until Close returns; new
	// readers can only obtain a manager after the replacement is active.
	managersMu.Lock()
	defer managersMu.Unlock()
	if manager, exists := managers[kbID]; exists {
		manager.Close()
		delete(managers, kbID)
	}
	activePath := filepath.Join(config.Get().GetKBDir(), fmt.Sprintf("%d", kbID), "vectors.db")
	backupPath := activePath + ".previous"
	_ = os.Remove(backupPath)
	if err := os.Rename(activePath, backupPath); err != nil {
		return fmt.Errorf("stage old vector store: %w", err)
	}
	if err := os.Rename(temporaryPath, activePath); err != nil {
		_ = os.Rename(backupPath, activePath)
		return fmt.Errorf("activate rebuilt vector store: %w", err)
	}
	_ = os.Remove(filepath.Join(config.Get().GetKBDir(), fmt.Sprintf("%d", kbID), "index.bin"))
	return nil
}

func finishEmbeddingRebuild(kbID int64, fingerprint string, count int, rebuildErr error) {
	updates := map[string]interface{}{"embedding_fingerprint": fingerprint, "embedding_dimension": embeddingDim, "embedding_rebuild_status": model.KnowledgeBaseEmbeddingRebuildReady, "embedding_rebuild_error": "", "vector_count": count, "index_type": model.KnowledgeBaseIndexTypeFlat}
	if rebuildErr != nil {
		delete(updates, "embedding_fingerprint")
		delete(updates, "embedding_dimension")
		delete(updates, "vector_count")
		delete(updates, "index_type")
		updates["embedding_rebuild_status"] = model.KnowledgeBaseEmbeddingRebuildFailed
		updates["embedding_rebuild_error"] = rebuildErr.Error()
	}
	if err := database.DB.Model(&model.KnowledgeBase{}).Where("id = ?", kbID).Updates(updates).Error; err != nil {
		applogger.Error("KB embedding rebuild state update failed", "kb_id", kbID, "error", err)
		return
	}
	if rebuildErr != nil {
		applogger.Error("KB embedding rebuild failed; retained previous vectors", "kb_id", kbID, "error", rebuildErr)
		return
	}
	applogger.Info("KB embedding rebuild completed", "kb_id", kbID, "vector_count", count)
}
