package kb

import (
	"context"
	"fmt"
	"path/filepath"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"

	"gorm.io/gorm"
)

const (
	defaultchunkSize    = 500
	defaultchunkOverlap = 50
	defaultMinchunkSize = 100
	embedBatchSize      = 10
)

// documentProcessor handles the document processing pipeline:
// extract text → split into chunks → generate embeddings → store vectors.
//
// The database connection is obtained from the database package directly.
type documentProcessor struct {
	splitter   *textSplitter
	embService *llm.EmbeddingService
}

// newDocumentProcessor creates a documentProcessor with the given embedding service.
func newDocumentProcessor(embService *llm.EmbeddingService) *documentProcessor {
	return &documentProcessor{
		splitter:   newTextSplitter(defaultchunkSize, defaultchunkOverlap, defaultMinchunkSize),
		embService: embService,
	}
}

// Process executes the full document processing pipeline.
// Steps: extract → split → store chunks → embed → store vectors.
func (dp *documentProcessor) Process(ctx context.Context, kbID int64, doc *model.Document) error {
	dp.updateStatus(doc.ID, model.DocumentStatusProcessing, "")

	text, err := Extract(doc.FilePath)
	if err != nil {
		dp.cleanupchunks(doc.ID)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Extraction failed: %v", err))
		return err
	}

	chunks := dp.splitter.Split(text)
	if len(chunks) == 0 {
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, "No text content extracted")
		return fmt.Errorf("no text content extracted from document %d", doc.ID)
	}

	chunkModels := dp.storechunks(kbID, doc.ID, chunks)

	embeddings, err := dp.generateEmbeddings(ctx, chunkModels)
	if err != nil {
		dp.cleanupchunks(doc.ID)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Embedding failed: %v", err))
		return err
	}

	vectorsDBPath := filepath.Join(config.Get().GetKBDir(), fmt.Sprintf("%d", kbID), "vectors.db")
	vs, err := newVectorStore(vectorsDBPath)
	if err != nil {
		dp.cleanupchunks(doc.ID)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Vector store error: %v", err))
		return err
	}
	defer vs.Close()

	entries := make([]vectorEntry, len(chunkModels))
	for i, cm := range chunkModels {
		entries[i] = vectorEntry{
			ChunkID:   cm.ID,
			Embedding: embeddings[i],
		}
	}
	if err := vs.InsertBatch(entries); err != nil {
		dp.cleanupchunks(doc.ID)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Vector insert failed: %v", err))
		return err
	}

	for i := range chunkModels {
		if err := database.DB.Model(&model.DocumentChunk{}).Where("id = ?", chunkModels[i].ID).
			Update("vector_id", 1).Error; err != nil {
			applogger.Error("failed to update vector_id for chunk", "chunk_id", chunkModels[i].ID, "error", err)
		}
	}

	if err := database.DB.Model(&model.KnowledgeBase{}).Where("id = ?", kbID).
		Update("vector_count", gorm.Expr("vector_count + ?", len(chunkModels))).Error; err != nil {
		applogger.Error("failed to update KB vector_count", "kb_id", kbID, "error", err)
	}

	dp.updateStatus(doc.ID, model.DocumentStatusReady, "")
	if err := database.DB.Model(&model.Document{}).Where("id = ?", doc.ID).Update("chunk_count", len(chunkModels)).Error; err != nil {
		applogger.Error("failed to update document chunk_count", "doc_id", doc.ID, "error", err)
	}
	if err := database.DB.Model(&model.KnowledgeBase{}).Where("id = ?", kbID).
		Update("document_count", gorm.Expr("document_count + 1")).Error; err != nil {
		applogger.Error("failed to update KB document_count", "kb_id", kbID, "error", err)
	}

	applogger.Info("Document processed successfully",
		"doc_id", doc.ID, "chunks", len(chunkModels))
	return nil
}

func (dp *documentProcessor) storechunks(kbID, docID int64, chunks []chunk) []model.DocumentChunk {
	models := make([]model.DocumentChunk, len(chunks))
	for i, c := range chunks {
		models[i] = model.DocumentChunk{
			KnowledgeBaseID: kbID,
			DocumentID:      docID,
			ChunkIndex:      c.chunkIndex,
			Content:         c.Content,
			StartOffset:     c.StartOffset,
			EndOffset:       c.EndOffset,
		}
	}
	if err := database.DB.Create(&models).Error; err != nil {
		applogger.Error("failed to create document chunks", "doc_id", docID, "count", len(models), "error", err)
	}
	return models
}

func (dp *documentProcessor) generateEmbeddings(ctx context.Context, chunks []model.DocumentChunk) ([][]float32, error) {
	var allEmbeddings [][]float32

	for i := 0; i < len(chunks); i += embedBatchSize {
		end := i + embedBatchSize
		if end > len(chunks) {
			end = len(chunks)
		}

		texts := make([]string, end-i)
		for j := i; j < end; j++ {
			texts[j-i] = chunks[j].Content
		}

		embeddings, err := dp.embService.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("embedding batch %d failed: %w", i/embedBatchSize, err)
		}
		allEmbeddings = append(allEmbeddings, embeddings...)
	}

	return allEmbeddings, nil
}

func (dp *documentProcessor) updateStatus(docID int64, status int, errMsg string) {
	updates := map[string]interface{}{
		"status": status,
	}
	if errMsg != "" {
		updates["error_message"] = errMsg
	}
	if err := database.DB.Model(&model.Document{}).Where("id = ?", docID).Updates(updates).Error; err != nil {
		applogger.Error("failed to update document status", "doc_id", docID, "status", status, "error", err)
	}
}

func (dp *documentProcessor) cleanupchunks(docID int64) {
	var chunkIDs []int64
	if err := database.DB.Model(&model.DocumentChunk{}).Where("document_id = ?", docID).Pluck("id", &chunkIDs).Error; err != nil {
		applogger.Error("failed to pluck chunk IDs for cleanup", "doc_id", docID, "error", err)
		return
	}
	if len(chunkIDs) > 0 {
		if err := database.DB.Delete(&model.DocumentChunk{}, chunkIDs).Error; err != nil {
			applogger.Error("failed to delete orphan chunks", "doc_id", docID, "count", len(chunkIDs), "error", err)
		}
		applogger.Info("Cleaned up orphan chunks", "doc_id", docID, "count", len(chunkIDs))
	}
}
