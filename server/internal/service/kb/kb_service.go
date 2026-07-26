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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/llm"

	_ "github.com/glebarez/go-sqlite/compat"
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

// Init initializes the kb package with embedding parameters.
// Must be called once at application startup before any other kb functions.
// The database connection is obtained from the database package directly.
func Init(embDim, flatThresh int) {
	managers = make(map[int64]*indexManager)
	workerCh = make(map[int64]chan int64)
	embeddingDim = embDim
	flatThreshold = flatThresh
}

// RecoverProcessingDocuments marks all documents with status=processing as failed.
// Called on startup to handle interrupted processing pipelines.
// Also recovers knowledge bases stuck in "switching" state.
func RecoverProcessingDocuments() {
	result := database.DB.Model(&model.Document{}).
		Where("status = ?", model.DocumentStatusProcessing).
		Update("status", model.DocumentStatusFailed)
	if result.RowsAffected > 0 {
		applogger.Info("Recovered processing documents", "count", result.RowsAffected)
	}

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

// DeleteKnowledgeBase deletes a knowledge base and all its data.
func DeleteKnowledgeBase(kbID int64) error {
	var configs []model.AgentConfig
	if err := database.DB.Find(&configs).Error; err != nil {
		return fmt.Errorf("failed to load agent configs for KB cleanup: %w", err)
	}
	for _, ac := range configs {
		var ids []int64
		if err := jsonUnmarshal(ac.KnowledgeBaseIDs, &ids); err != nil {
			applogger.Error("failed to unmarshal agent config knowledge base IDs during KB deletion", "agent_config_id", ac.ID, "error", err)
			continue
		}
		for i, id := range ids {
			if id == kbID {
				ids = append(ids[:i], ids[i+1:]...)
				if err := database.DB.Model(&ac).Update("knowledge_base_ids", jsonMarshal(ids)).Error; err != nil {
					applogger.Error("failed to update agent config KB IDs after KB deletion", "agent_config_id", ac.ID, "error", err)
				}
				break
			}
		}
	}

	releaseindexManager(kbID)

	kbDir := filepath.Join(config.Get().GetKBDir(), fmt.Sprintf("%d", kbID))
	os.RemoveAll(kbDir)

	if err := database.DB.Where("knowledge_base_id = ?", kbID).Delete(&model.DocumentChunk{}).Error; err != nil {
		applogger.Error("failed to delete document chunks for KB", "kb_id", kbID, "error", err)
	}
	if err := database.DB.Where("knowledge_base_id = ?", kbID).Delete(&model.Document{}).Error; err != nil {
		applogger.Error("failed to delete documents for KB", "kb_id", kbID, "error", err)
	}
	if err := database.DB.Delete(&model.KnowledgeBase{}, kbID).Error; err != nil {
		return fmt.Errorf("failed to delete knowledge base: %w", err)
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

// SubmitDocument submits a document for async processing.
func SubmitDocument(docID int64) {
	kbID := getDocumentKBID(docID)
	if kbID == 0 {
		return
	}
	ch := getWorkerChannel(kbID)
	ch <- docID
}

func getDocumentKBID(docID int64) int64 {
	var doc model.Document
	if err := database.DB.Select("knowledge_base_id").First(&doc, docID).Error; err != nil {
		return 0
	}
	return doc.KnowledgeBaseID
}

func getWorkerChannel(kbID int64) chan int64 {
	workerChMu.Lock()
	defer workerChMu.Unlock()
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

	if err := processor.Process(ctx, kbID, &doc); err != nil {
		applogger.Error("Document processing failed", "doc_id", docID, "error", err)
		return
	}

	addVectorsToIndex(kbID, docID)
}

// addVectorsToIndex loads newly created vectors for a document and adds them
// to the indexManager's in-memory index (HNSW graph or pending queue).
func addVectorsToIndex(kbID, docID int64) {
	mgr, err := getOrCreateIndexManager(kbID)
	if err != nil {
		applogger.Error("Failed to get index manager for vector update", "kb_id", kbID, "error", err)
		return
	}

	var chunkIDs []int64
	if err := database.DB.Model(&model.DocumentChunk{}).Where("document_id = ?", docID).Pluck("id", &chunkIDs).Error; err != nil {
		applogger.Error("failed to pluck chunk IDs for index update", "doc_id", docID, "error", err)
		return
	}

	for _, chunkID := range chunkIDs {
		embedding, err := mgr.GetVector(chunkID)
		if err != nil {
			applogger.Error("Failed to get embedding for index update", "chunk_id", chunkID, "error", err)
			continue
		}
		if embedding == nil {
			continue
		}
		if err := mgr.AddToIndex(uint64(chunkID), embedding); err != nil {
			applogger.Error("Failed to add vector to index", "chunk_id", chunkID, "error", err)
		}
	}
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
}

// SearchKB searches within a single knowledge base.
func SearchKB(ctx context.Context, kbID int64, query string, topK int) ([]schema.SearchResult, error) {
	return searchKB(ctx, kbID, query, topK)
}

// SearchMultiKB searches across multiple knowledge bases.
func SearchMultiKB(ctx context.Context, kbIDs []int64, query string, topK int) ([]schema.SearchResult, error) {
	return searchMultiKB(ctx, kbIDs, query, topK)
}

func createVectorsDB(path string) error {
	sqlDB, err := sql.Open("sqlite3", path)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	_, err = sqlDB.Exec(`
		CREATE TABLE IF NOT EXISTS vectors (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chunk_id INTEGER NOT NULL UNIQUE,
			embedding BLOB NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	return err
}

func jsonUnmarshal(data string, v interface{}) error {
	return json.Unmarshal([]byte(data), v)
}

func jsonMarshal(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
