// Package kb provides knowledge base management, document processing, and retrieval.
//
// This is a package-level singleton service. Call Init(embDim, flatThreshold) once at
// startup, then RecoverProcessingDocuments() to handle interrupted pipelines. Public
// functions (SearchKB, SearchMultiKB, SubmitDocument, etc.) can be called directly.
//
// # Architecture
//
// Each knowledge base has isolated storage:
//
//	{KBDir}/{kbID}/
//	  ├── files/        Original uploaded documents
//	  ├── vectors.db    Vector storage (SQLite, one row per chunk)
//	  └── index.bin     HNSW graph (serialized, loaded on demand)
//
// An indexManager (index_manager.go) per KB manages the in-memory index state,
// loaded lazily on first access and released on KB deletion or shutdown.
//
// # Document processing pipeline
//
// Each KB has a dedicated worker goroutine (buffer=64) processing documents serially:
//
//  1. Extract: Extract(doc.FilePath) translates PDF/DOCX/TXT into plain text
//     (text_extractor.go).
//  2. Split: textSplitter uses recursive character splitting with configurable
//     chunk size (500), overlap (50), and minimum chunk size (100)
//     (text_splitter.go).
//  3. Store chunks: writes DocumentChunk records to the main DB.
//  4. Embed: batch-embeds chunks (batch size 10) via the configured embedding service.
//  5. Store vectors: inserts vectors into the KB's vectors.db via vectorStore.
//
// Steps 1–5 are executed by documentProcessor.Process (document_processor.go).
// After Process returns, the worker in kb_service.go calls addVectorsToIndex
// to add vectors to the indexManager's in-memory index.
//
// Status transitions: pending → processing → ready / failed. Error cleanup removes
// orphaned chunks (cleanupchunks).
//
// # Dual index strategy (index_manager.go)
//
//   - Flat (brute-force): cosine similarity against all vectors. Used when vector
//     count < flatThreshold.
//   - HNSW (approximate nearest neighbor): switched to automatically when vector
//     count ≥ flatThreshold. The switch is non-blocking — while the HNSW graph
//     is built (buildHNSWIndex), new vectors go into a pending queue and are
//     merged into the final graph before activation.
//   - Switching state (indexTypeSwitching): KB is temporarily in a transition
//     state. Searches fall back to flat. On startup, any KB stuck in switching
//     is reset to flat (RecoverProcessingDocuments).
//   - Graph persistence: the HNSW graph is serialized to index.bin via atomic
//     rename (write to .tmp → rename).
//
// # Retrieval (retriever.go)
//
//   - searchKB: single-KB search. Generates query embedding, calls
//     indexManager.Search, filters out deleted chunks, resolves chunk→result.
//   - searchMultiKB: concurrent search across multiple KBs. Each KB search
//     runs in its own goroutine; results are merged from the channel.
//   - Deleted vector filtering: deleted chunks have Deleted=1 in document_chunks
//     but may still have vectors. A deletedVectorTracker filters them post-search.
//
// # Startup recovery (RecoverProcessingDocuments)
//
//   - Documents with status=processing are marked failed (interrupted pipeline).
//   - Knowledge bases with index_type=switching are reset to flat (interrupted HNSW build).
//
// # Deletion (DeleteKnowledgeBase)
//
// When a KB is deleted:
//   - All kb_access grants referencing this KB are removed (dops.DeleteAccessByKB).
//   - The indexManager is released from memory.
//   - The KB directory (files + vectors.db + index.bin) is removed.
//   - DocumentChunk and Document rows are deleted from the main DB.
//
// # Shutdown
//
// Shutdown() closes all worker channels, letting goroutines exit when channels
// close, then closes all index managers (releasing vectors.db connections and HNSW graphs).
package kb
