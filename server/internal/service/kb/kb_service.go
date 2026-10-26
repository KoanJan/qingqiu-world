// Package kb provides knowledge base management services including document
// processing, vector storage, and retrieval-augmented generation (RAG).
//
// This package is designed as a package-level service: call Init() once at
// startup, then use package-level functions (SearchKB, SearchMultiKB, etc.)
// directly. No struct instances need to be created or passed around.
package kb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/llm"

	_ "github.com/glebarez/go-sqlite/compat"
	"gorm.io/gorm"
)

const (
	// DefaultEmbeddingDim is the default embedding vector dimension (e.g., text-embedding-3-large).
	DefaultEmbeddingDim = 1536

	// DefaultSearchTopK is the default number of top results returned by KB search.
	DefaultSearchTopK = 5
)

var (
	managers      map[int64]*indexManager
	managersMu    sync.RWMutex
	workerCh      map[int64]chan int64
	workerChMu    sync.Mutex
	embeddingDim  int
	flatThreshold int
)

const recoveryEnqueueRetryDelay = 50 * time.Millisecond

// Init initializes the kb package with embedding parameters.
// Must be called once at application startup before any other kb functions.
// The database connection is obtained from the database package directly.
func Init(embDim, flatThresh int) {
	managers = make(map[int64]*indexManager)
	workerCh = make(map[int64]chan int64)
	embeddingDim = embDim
	flatThreshold = flatThresh
	recoverEmbeddingGenerations()
}

// RecoverProcessingDocuments restores safe serving state after an interrupted
// process. Pending uploads are re-enqueued without changing their state;
// interrupted processing is marked failed so a later upload can create a new
// revision; ready documents without an active revision are verified before
// activation or made explicitly unavailable. It also recovers KBs stuck in an
// interrupted HNSW switch.
func RecoverProcessingDocuments() {
	result := database.DB.Model(&model.Document{}).
		Where("status = ?", model.DocumentStatusProcessing).
		Update("status", model.DocumentStatusFailed)
	if result.RowsAffected > 0 {
		applogger.Info("Recovered processing documents", "count", result.RowsAffected)
	}
	recoverPendingDocuments()
	recoverReadyInactiveDocuments()

	var switchingKBs []model.KnowledgeBase
	if err := database.DB.Where("index_type = ?", model.KnowledgeBaseIndexTypeSwitching).Find(&switchingKBs).Error; err != nil {
		applogger.Error("failed to load switching KBs for recovery", "error", err)
		return
	}
	for _, kb := range switchingKBs {
		applogger.Info("Recovering switching KB, resetting to flat", "kb_id", kb.ID)
		if err := database.DB.Model(&kb).Update("index_type", model.KnowledgeBaseIndexTypeFlat).Error; err != nil {
			applogger.Error("failed to reset KB index type to flat", "kb_id", kb.ID, "error", err)
		}
	}
}

// recoverPendingDocuments re-enqueues only durable local uploads that were
// accepted before the previous process stopped. A background producer is used
// only at startup so a full per-KB worker channel cannot strand later pending
// documents; it exits when Shutdown removes the channel map.
func recoverPendingDocuments() {
	var documents []model.Document
	if err := database.DB.Where("status = ?", model.DocumentStatusPending).
		Order("knowledge_base_id ASC, id ASC").Find(&documents).Error; err != nil {
		applogger.Error("failed to load pending documents for startup recovery", "error", err)
		return
	}
	if len(documents) == 0 {
		return
	}

	byKB := make(map[int64][]int64)
	for _, document := range documents {
		if document.SourceKind != model.DocumentSourceKindLocalUpload {
			markDocumentFailed(document.ID, fmt.Sprintf("Unsupported source kind %d during startup recovery", document.SourceKind))
			applogger.Warn("pending document has unsupported source kind", "kb_id", document.KnowledgeBaseID, "doc_id", document.ID, "source_kind", document.SourceKind)
			continue
		}
		if _, err := os.Stat(document.FilePath); err != nil {
			markDocumentFailed(document.ID, fmt.Sprintf("Local upload is unavailable during startup recovery: %v", err))
			applogger.Warn("pending local upload cannot be re-enqueued", "kb_id", document.KnowledgeBaseID, "doc_id", document.ID, "path", document.FilePath, "error", err)
			continue
		}
		byKB[document.KnowledgeBaseID] = append(byKB[document.KnowledgeBaseID], document.ID)
	}

	queuedCount := 0
	for kbID, documentIDs := range byKB {
		queueRecoveredDocuments(kbID, documentIDs)
		queuedCount += len(documentIDs)
	}
	applogger.Info("Queued pending documents for startup recovery", "document_count", queuedCount, "knowledge_base_count", len(byKB))
}

// queueRecoveredDocuments waits for per-KB queue capacity without blocking
// startup. It holds workerChMu while sending, which makes its send mutually
// exclusive with Shutdown closing the channel.
func queueRecoveredDocuments(kbID int64, documentIDs []int64) {
	go func() {
		for _, documentID := range documentIDs {
			for {
				workerChMu.Lock()
				channel, exists := workerCh[kbID]
				if !exists {
					workerChMu.Unlock()
					applogger.Info("stopped pending document recovery during shutdown", "kb_id", kbID, "doc_id", documentID)
					return
				}
				select {
				case channel <- documentID:
					workerChMu.Unlock()
					applogger.Debug("re-enqueued pending document", "kb_id", kbID, "doc_id", documentID)
					goto queued
				default:
					workerChMu.Unlock()
					time.Sleep(recoveryEnqueueRetryDelay)
				}
			}
		queued:
		}
	}()
}

// recoverReadyInactiveDocuments restores only a revision whose persisted
// chunks, provenance, and vectors can still be verified. A ready document
// without such a revision is failed rather than made visible with incomplete
// serving artifacts.
func recoverReadyInactiveDocuments() {
	var documents []model.Document
	if err := database.DB.Where("status = ? AND active_revision_id = 0", model.DocumentStatusReady).
		Order("knowledge_base_id ASC, id ASC").Find(&documents).Error; err != nil {
		applogger.Error("failed to load ready inactive documents for startup recovery", "error", err)
		return
	}
	for _, document := range documents {
		if err := activateVerifiedReadyRevision(document); err != nil {
			message := fmt.Sprintf("Ready document recovery failed: %v", err)
			markDocumentFailed(document.ID, message)
			applogger.Error("ready inactive document could not be recovered", "kb_id", document.KnowledgeBaseID, "doc_id", document.ID, "error", err)
		}
	}
	if len(documents) > 0 {
		applogger.Info("Ready inactive document recovery completed", "document_count", len(documents))
	}
}

// activateVerifiedReadyRevision verifies the newest usable revision before it
// becomes visible. It deliberately checks the persisted vector rows rather
// than assuming a non-zero vector marker proves the vector database survived.
func activateVerifiedReadyRevision(document model.Document) error {
	var revision model.DocumentRevision
	if err := database.DB.Where("document_id = ? AND status = ? AND provenance_status = ?", document.ID, model.DocumentRevisionStatusReady, model.DocumentRevisionProvenanceVerified).
		Order("id DESC").First(&revision).Error; err != nil {
		return fmt.Errorf("load verified ready revision: %w", err)
	}
	var chunks []model.DocumentChunk
	if err := database.DB.Where("document_id = ? AND revision_id = ? AND deleted = 0", document.ID, revision.ID).
		Order("id ASC").Find(&chunks).Error; err != nil {
		return fmt.Errorf("load revision chunks: %w", err)
	}
	if len(chunks) == 0 {
		return fmt.Errorf("verified revision %d has no active chunks", revision.ID)
	}
	manager, err := getOrCreateIndexManager(document.KnowledgeBaseID)
	if err != nil {
		return fmt.Errorf("load vector index: %w", err)
	}
	for _, chunk := range chunks {
		if chunk.VectorID == 0 {
			return fmt.Errorf("chunk %d has no persisted vector marker", chunk.ID)
		}
		vector, err := manager.GetVector(chunk.ID)
		if err != nil {
			return fmt.Errorf("verify vector for chunk %d: %w", chunk.ID, err)
		}
		if len(vector) == 0 {
			return fmt.Errorf("verify vector for chunk %d: vector is empty", chunk.ID)
		}
	}
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.Document{}).Where("id = ? AND status = ? AND active_revision_id = 0", document.ID, model.DocumentStatusReady).
			Update("active_revision_id", revision.ID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("document visibility changed during recovery")
		}
		return nil
	}); err != nil {
		return fmt.Errorf("activate verified revision: %w", err)
	}
	if err := updateBM25ForDocument(document.KnowledgeBaseID, document.ID); err != nil {
		return fmt.Errorf("restore BM25 visibility: %w", err)
	}
	applogger.Info("Activated verified ready revision during startup recovery", "kb_id", document.KnowledgeBaseID, "doc_id", document.ID, "revision_id", revision.ID)
	return nil
}

// CreateKnowledgeBase creates a new knowledge base with its storage directories.
func CreateKnowledgeBase(kb *model.KnowledgeBase) error {
	if err := database.DB.Create(kb).Error; err != nil {
		return fmt.Errorf("failed to create knowledge base: %w", err)
	}

	kbDir := filepath.Join(config.Get().GetKBDir(), fmt.Sprintf("%d", kb.ID))
	filesDir := filepath.Join(kbDir, "files")
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		return fmt.Errorf("failed to create kb directories: %w", err)
	}

	vectorsDBPath := filepath.Join(kbDir, "vectors.db")
	if err := createVectorsDB(vectorsDBPath); err != nil {
		os.RemoveAll(kbDir)
		return fmt.Errorf("failed to create vectors database: %w", err)
	}

	indexFilePath := filepath.Join(kbDir, "index.bin")
	kb.IndexFilePath = indexFilePath
	if err := database.DB.Model(kb).Update("index_file_path", indexFilePath).Error; err != nil {
		os.RemoveAll(kbDir)
		return fmt.Errorf("failed to update index file path: %w", err)
	}

	return nil
}

// getOrCreateIndexManager returns the indexManager for a knowledge base,
// loading it lazily on first access.
func getOrCreateIndexManager(kbID int64) (*indexManager, error) {
	managersMu.RLock()
	m, ok := managers[kbID]
	managersMu.RUnlock()
	if ok {
		return m, nil
	}

	managersMu.Lock()
	defer managersMu.Unlock()

	if m, ok = managers[kbID]; ok {
		return m, nil
	}

	var kb model.KnowledgeBase
	if err := database.DB.First(&kb, kbID).Error; err != nil {
		return nil, fmt.Errorf("knowledge base not found: %w", err)
	}

	vectorsDBPath := filepath.Join(config.Get().GetKBDir(), fmt.Sprintf("%d", kbID), "vectors.db")
	m = newIndexManager(kb.IndexType, kb.IndexFilePath, vectorsDBPath, kbID, flatThreshold)
	if err := m.Load(); err != nil {
		return nil, fmt.Errorf("failed to load index manager: %w", err)
	}

	managers[kbID] = m
	return m, nil
}

func releaseindexManager(kbID int64) {
	managersMu.Lock()
	defer managersMu.Unlock()
	if m, ok := managers[kbID]; ok {
		m.Close()
		delete(managers, kbID)
	}
}

// SubmitDocument queues a document without blocking an HTTP handler forever.
// A full queue is returned to the caller so the pending document can be
// retried instead of silently disappearing behind a blocked request.
func SubmitDocument(docID int64) error {
	kbID, err := getDocumentKBID(docID)
	if err != nil {
		return err
	}

	workerChMu.Lock()
	defer workerChMu.Unlock()
	ch := getWorkerChannelLocked(kbID)
	select {
	case ch <- docID:
		return nil
	default:
		return fmt.Errorf("knowledge base processing queue is full")
	}
}

// getDocumentKBID resolves a document's KB and returns a logged caller error
// rather than treating missing data as an implicit zero-value KB.
func getDocumentKBID(docID int64) (int64, error) {
	var doc model.Document
	if err := database.DB.Select("knowledge_base_id").First(&doc, docID).Error; err != nil {
		return 0, fmt.Errorf("load document %d knowledge base: %w", docID, err)
	}
	return doc.KnowledgeBaseID, nil
}

// getWorkerChannelLocked obtains a per-KB serial worker channel. The caller
// holds workerChMu so Shutdown cannot close a channel while it is being used.
func getWorkerChannelLocked(kbID int64) chan int64 {
	if ch, ok := workerCh[kbID]; ok {
		return ch
	}
	ch := make(chan int64, 64)
	workerCh[kbID] = ch
	go worker(kbID, ch)
	return ch
}

func worker(kbID int64, ch chan int64) {
	ctx := context.Background()
	for docID := range ch {
		processDocument(ctx, kbID, docID)
	}
}

func processDocument(ctx context.Context, kbID, docID int64) {
	applogger.Info("Processing document", "kb_id", kbID, "doc_id", docID)

	var doc model.Document
	if err := database.DB.First(&doc, docID).Error; err != nil {
		applogger.Error("Document not found", "doc_id", docID, "error", err)
		return
	}
	if doc.Status != model.DocumentStatusPending {
		applogger.Warn("Skipped queued document with unexpected status", "kb_id", kbID, "doc_id", docID, "status", doc.Status)
		return
	}
	if doc.SourceKind != model.DocumentSourceKindLocalUpload {
		message := fmt.Sprintf("Unsupported source kind %d", doc.SourceKind)
		markDocumentFailed(docID, message)
		applogger.Warn("Rejected queued document with unsupported source kind", "kb_id", kbID, "doc_id", docID, "source_kind", doc.SourceKind)
		return
	}

	var kb model.KnowledgeBase
	if err := database.DB.First(&kb, kbID).Error; err != nil {
		applogger.Error("Knowledge base not found", "kb_id", kbID, "error", err)
		return
	}

	embConfig := dops.GetEmbeddingConfig()
	if embConfig == nil {
		if err := database.DB.Model(&model.Document{}).Where("id = ?", docID).Updates(map[string]interface{}{
			"status":        model.DocumentStatusFailed,
			"error_message": "Embedding config not found",
		}).Error; err != nil {
			applogger.Error("failed to mark document as failed", "doc_id", docID, "error", err)
		}
		return
	}

	embService := llm.NewEmbeddingService(embConfig.BaseURL, embConfig.APIKey, embConfig.ModelID, embeddingDim)
	processor := newDocumentProcessor(embService)

	revision, err := processor.Process(ctx, kbID, &doc)
	if err != nil {
		applogger.Error("Document processing failed", "doc_id", docID, "error", err)
		return
	}

	if err := addVectorsToIndex(kbID, docID); err != nil {
		markDocumentFailed(docID, fmt.Sprintf("Serving vector index update failed: %v", err))
		return
	}
	if err := updateBM25ForDocument(kbID, docID); err != nil {
		markDocumentFailed(docID, fmt.Sprintf("Serving BM25 update failed: %v", err))
		return
	}
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if revision.ProvenanceStatus != model.DocumentRevisionProvenanceVerified {
			return fmt.Errorf("revision provenance is not verified")
		}
		result := tx.Model(&model.DocumentRevision{}).Where("id = ? AND provenance_status = ?", revision.ID, model.DocumentRevisionProvenanceVerified).Updates(map[string]interface{}{
			"status":        model.DocumentRevisionStatusReady,
			"error_message": "",
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("revision provenance verification was lost before activation")
		}
		return tx.Model(&model.Document{}).Where("id = ?", docID).Updates(map[string]interface{}{
			"status":             model.DocumentStatusReady,
			"error_message":      "",
			"active_revision_id": revision.ID,
		}).Error
	}); err != nil {
		processor.updateRevisionStatus(revision.ID, model.DocumentRevisionStatusFailed, fmt.Sprintf("Activation failed: %v", err))
		markDocumentFailed(docID, fmt.Sprintf("Revision activation failed: %v", err))
		applogger.Error("failed to activate document revision after serving indexes updated", "kb_id", kbID, "doc_id", docID, "revision_id", revision.ID, "error", err)
		return
	}
	if err := MarkRelationsStaleForSupersededDocumentRevisions(docID, revision.ID, "document active revision changed"); err != nil {
		applogger.Error("failed to mark superseded document relations stale", "kb_id", kbID, "doc_id", docID, "revision_id", revision.ID, "error", err)
	}
	applogger.Info("Document revision activated after serving indexes updated", "kb_id", kbID, "doc_id", docID, "revision_id", revision.ID)
	// The first vector batch establishes this KB's generation. Existing KBs
	// with unknown legacy vectors are deliberately not stamped here; startup
	// rebuilds them before a new fingerprint can be trusted.
	if kb.VectorCount == 0 {
		if embeddingConfig := dops.GetEmbeddingConfig(); embeddingConfig != nil {
			if err := database.DB.Model(&model.KnowledgeBase{}).Where("id = ? AND embedding_fingerprint = ''", kbID).Updates(map[string]interface{}{
				"embedding_fingerprint":    embeddingFingerprint(embeddingConfig),
				"embedding_dimension":      embeddingDim,
				"embedding_rebuild_status": model.KnowledgeBaseEmbeddingRebuildReady,
				"embedding_rebuild_error":  "",
			}).Error; err != nil {
				applogger.Error("failed to record initial KB embedding generation", "kb_id", kbID, "error", err)
			}
		}
	}
}

// markDocumentFailed records a serving-path failure so callers never observe
// a ready document whose BM25 or vector index update did not complete.
func markDocumentFailed(docID int64, message string) {
	if err := database.DB.Model(&model.Document{}).Where("id = ?", docID).Updates(map[string]interface{}{
		"status":        model.DocumentStatusFailed,
		"error_message": message,
	}).Error; err != nil {
		applogger.Error("failed to mark document as failed", "doc_id", docID, "error", err)
	}
}

// addVectorsToIndex loads newly created vectors for a document and adds them
// to the indexManager's in-memory index (HNSW graph or pending queue).
func addVectorsToIndex(kbID, docID int64) error {
	mgr, err := getOrCreateIndexManager(kbID)
	if err != nil {
		return fmt.Errorf("get index manager: %w", err)
	}

	var chunkIDs []int64
	if err := database.DB.Model(&model.DocumentChunk{}).Where("document_id = ?", docID).Pluck("id", &chunkIDs).Error; err != nil {
		return fmt.Errorf("load document chunk IDs: %w", err)
	}
	if len(chunkIDs) == 0 {
		return fmt.Errorf("document has no chunks")
	}

	for _, chunkID := range chunkIDs {
		embedding, err := mgr.GetVector(chunkID)
		if err != nil {
			return fmt.Errorf("load vector for chunk %d: %w", chunkID, err)
		}
		if embedding == nil {
			return fmt.Errorf("missing persisted vector for chunk %d", chunkID)
		}
		if err := mgr.AddToIndex(uint64(chunkID), embedding); err != nil {
			return fmt.Errorf("add vector for chunk %d: %w", chunkID, err)
		}
	}
	return nil
}

// Shutdown stops all worker goroutines and releases index managers.
func Shutdown() {
	workerChMu.Lock()
	for _, ch := range workerCh {
		close(ch)
	}
	workerCh = make(map[int64]chan int64)
	workerChMu.Unlock()

	managersMu.Lock()
	for _, m := range managers {
		m.Close()
	}
	managers = make(map[int64]*indexManager)
	managersMu.Unlock()

	releaseAllBM25Indexes()
}

// SearchKB searches within a single knowledge base.
func SearchKB(ctx context.Context, kbID int64, query string, topK int) ([]schema.SearchResult, error) {
	return searchKB(ctx, kbID, query, topK)
}

// SearchMultiKB searches across multiple knowledge bases.
func SearchMultiKB(ctx context.Context, kbIDs []int64, query string, topK int) ([]schema.SearchResult, error) {
	return searchMultiKB(ctx, kbIDs, query, topK, "")
}

func createVectorsDB(path string) error {
	sqlDB, err := sql.Open("sqlite3", path)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	return ensureVectorTableSchema(sqlDB)
}
