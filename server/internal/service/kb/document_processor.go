package kb

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"

	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/kb/format"
	"qingqiu-world-server/internal/service/llm"

	"gorm.io/gorm"
)

const (
	embedBatchSize             = 10
	canonicalParserVersion     = "local-text-v1"
	canonicalNormalizerVersion = "normalizer-v2"
)

// documentProcessor handles the document processing pipeline:
// extract text → split into chunks → generate embeddings → store vectors.
//
// The database connection is obtained from the database package directly.
type documentProcessor struct {
	splitter   *textSplitter
	embService *llm.EmbeddingService
	profile    retrievalTokenProfile
}

// newDocumentProcessor creates a documentProcessor with the given embedding service.
func newDocumentProcessor(embService *llm.EmbeddingService) *documentProcessor {
	return &documentProcessor{
		embService: embService,
	}
}

// Process executes the full document processing pipeline.
// Steps: extract → split → store chunks → embed → store vectors.
// It returns the processing revision, which the caller activates only after
// both serving indexes have accepted the new retrieval units.
func (dp *documentProcessor) Process(ctx context.Context, kbID int64, doc *model.Document) (*model.DocumentRevision, error) {
	dp.updateStatus(doc.ID, model.DocumentStatusProcessing, "")
	profile, err := newRetrievalTokenProfile(config.Get())
	if err != nil {
		applogger.Error("invalid KB retrieval token profile", "document_id", doc.ID, "error", err)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Invalid retrieval token profile: %v", err))
		return nil, err
	}
	dp.profile = profile
	dp.splitter = newTextSplitter(profile.MaxTokens, profile.OverlapTokens, profile.MinTokens)
	if err := dp.splitter.Err(); err != nil {
		applogger.Error("KB tokenizer initialization failed", "document_id", doc.ID, "error", err)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Tokenizer initialization failed: %v", err))
		return nil, err
	}

	rendered, err := ExtractDocument(doc.FilePath)
	if err != nil {
		dp.cleanupchunks(doc.ID)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Extraction failed: %v", err))
		return nil, err
	}
	text := rendered.Text

	adapter, err := format.Select(rendered.FileType)
	if err != nil {
		applogger.Error("KB format adapter selection failed", "document_id", doc.ID, "file_type", rendered.FileType, "error", err)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Parser selection failed: %v", err))
		return nil, err
	}
	revision, err := dp.createProcessingRevision(doc, text, adapter.ID(), profile)
	if err != nil {
		applogger.Error("KB revision creation failed", "document_id", doc.ID, "error", err)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Revision creation failed: %v", err))
		return nil, err
	}
	pages := make([]format.Page, len(rendered.pages))
	for index, page := range rendered.pages {
		pages[index] = format.Page{Number: page.Number, Start: page.Start, End: page.End}
	}
	parsed, err := adapter.Parse(format.Document{Text: rendered.Text, FileType: rendered.FileType, Pages: pages})
	if err != nil {
		applogger.Error("KB structure parsing failed", "document_id", doc.ID, "adapter", adapter.ID(), "error", err)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Structure parsing failed: %v", err))
		dp.updateRevisionStatus(revision.ID, model.DocumentRevisionStatusFailed, fmt.Sprintf("Structure parsing failed: %v", err))
		return nil, err
	}
	tree, err := buildContentTree(doc.Title, text, parsedBlocksFromFormat(parsed), profile, func(value string) int { return len(dp.splitter.tp.Encode(value, nil, nil)) })
	if err != nil {
		applogger.Error("KB content tree construction failed", "document_id", doc.ID, "error", err)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Content tree construction failed: %v", err))
		dp.updateRevisionStatus(revision.ID, model.DocumentRevisionStatusFailed, fmt.Sprintf("Content tree construction failed: %v", err))
		return nil, err
	}
	generated, err := generateChunks(tree, doc.Title, dp.splitter, profile)
	if err != nil || len(generated) == 0 {
		if err == nil {
			err = fmt.Errorf("no retrieval leaves generated")
		}
		applogger.Error("KB chunk generation failed", "document_id", doc.ID, "error", err)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, "No text content extracted")
		dp.updateRevisionStatus(revision.ID, model.DocumentRevisionStatusFailed, "No text content extracted")
		return nil, err
	}
	chunks := make([]chunk, len(generated))
	for i, unit := range generated {
		chunks[i] = chunk{Content: unit.content, chunkIndex: i, StartOffset: unit.start, EndOffset: unit.end}
	}
	if err := validateChunkProvenance(text, chunks); err != nil {
		message := fmt.Sprintf("Source provenance validation failed: %v", err)
		applogger.Error("document parsing produced invalid source provenance", "document_id", doc.ID, "error", err)
		dp.updateRevisionProvenance(revision.ID, model.DocumentRevisionProvenanceRepairRequired, message)
		dp.updateRevisionStatus(revision.ID, model.DocumentRevisionStatusFailed, message)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, message)
		return nil, err
	}

	var chunkModels []model.DocumentChunk
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		if err := dp.storeContentTree(tx, doc.ID, revision.ID, rendered, tree); err != nil {
			return err
		}
		models, err := dp.storeGeneratedChunks(tx, kbID, doc.ID, revision.ID, doc.Title, rendered, generated)
		if err != nil {
			return err
		}
		for index, unit := range generated {
			if unit.leaf.leafPersistedID == 0 {
				return fmt.Errorf("generated chunk %d has no persisted leaf", index)
			}
			if err := tx.Create(&model.DocumentChunkNode{ChunkID: models[index].ID, NodeID: unit.leaf.leafPersistedID, Ordinal: 0}).Error; err != nil {
				return fmt.Errorf("create chunk-node mapping: %w", err)
			}
		}
		chunkModels = models
		return nil
	})
	if err != nil {
		applogger.Error("KB tree/chunk/mapping transaction failed", "document_id", doc.ID, "revision_id", revision.ID, "error", err)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Chunk storage failed: %v", err))
		dp.updateRevisionStatus(revision.ID, model.DocumentRevisionStatusFailed, fmt.Sprintf("Chunk storage failed: %v", err))
		return nil, err
	}
	if err := dp.updateRevisionProvenance(revision.ID, model.DocumentRevisionProvenanceVerified, ""); err != nil {
		dp.cleanupRevisionArtifacts(doc.ID, revision.ID)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Provenance status update failed: %v", err))
		dp.updateRevisionStatus(revision.ID, model.DocumentRevisionStatusFailed, fmt.Sprintf("Provenance status update failed: %v", err))
		return nil, err
	}
	revision.ProvenanceStatus = model.DocumentRevisionProvenanceVerified

	embeddings, err := dp.generateEmbeddings(ctx, chunkModels)
	if err != nil {
		dp.cleanupRevisionArtifacts(doc.ID, revision.ID)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Embedding failed: %v", err))
		dp.updateRevisionStatus(revision.ID, model.DocumentRevisionStatusFailed, fmt.Sprintf("Embedding failed: %v", err))
		return nil, err
	}

	vectorsDBPath := filepath.Join(config.Get().GetKBDir(), fmt.Sprintf("%d", kbID), "vectors.db")
	vs, err := newVectorStore(vectorsDBPath)
	if err != nil {
		dp.cleanupRevisionArtifacts(doc.ID, revision.ID)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Vector store error: %v", err))
		dp.updateRevisionStatus(revision.ID, model.DocumentRevisionStatusFailed, fmt.Sprintf("Vector store error: %v", err))
		return nil, err
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
		dp.cleanupRevisionArtifacts(doc.ID, revision.ID)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Vector insert failed: %v", err))
		dp.updateRevisionStatus(revision.ID, model.DocumentRevisionStatusFailed, fmt.Sprintf("Vector insert failed: %v", err))
		return nil, err
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

	if err := database.DB.Model(&model.Document{}).Where("id = ?", doc.ID).Update("chunk_count", len(chunkModels)).Error; err != nil {
		applogger.Error("failed to update document chunk_count", "doc_id", doc.ID, "error", err)
	}
	if err := database.DB.Model(&model.KnowledgeBase{}).Where("id = ?", kbID).
		Update("document_count", gorm.Expr("document_count + 1")).Error; err != nil {
		applogger.Error("failed to update KB document_count", "kb_id", kbID, "error", err)
	}

	applogger.Info("Document artifacts persisted and awaiting serving-index activation",
		"doc_id", doc.ID, "chunks", len(chunkModels))
	return revision, nil
}

// validateChunkProvenance verifies that every freshly parsed retrieval unit is
// an exact, non-empty range of the same canonical source rendition. Serving a
// revision without this invariant would make evidence metadata untrustworthy.
func validateChunkProvenance(text string, chunks []chunk) error {
	for _, chunk := range chunks {
		if chunk.Content == "" {
			return fmt.Errorf("chunk %d is empty", chunk.chunkIndex)
		}
		if chunk.StartOffset < 0 || chunk.EndOffset < chunk.StartOffset || chunk.EndOffset > len(text) {
			return fmt.Errorf("chunk %d has invalid range [%d,%d) for %d-byte source", chunk.chunkIndex, chunk.StartOffset, chunk.EndOffset, len(text))
		}
		if normalizeLocatorSearchText(text[chunk.StartOffset:chunk.EndOffset]).text != normalizeLocatorSearchText(chunk.Content).text {
			return fmt.Errorf("chunk %d content does not match canonical source range", chunk.chunkIndex)
		}
	}
	return nil
}

func (dp *documentProcessor) storeContentTree(tx *gorm.DB, docID, revisionID int64, rendered extractedDocument, root *contentTreeNode) error {
	var persist func(*contentTreeNode, int64, int) error
	persist = func(node *contentTreeNode, parentID int64, ordinal int) error {
		persisted := model.ContentNode{DocumentID: docID, RevisionID: revisionID, ParentID: parentID, Ordinal: ordinal, NodeType: node.NodeType, Text: node.Text, ContentHash: canonicalFingerprint("node-v2", node.Text), LocatorJSON: rendered.nodeLocatorJSON(node), MetadataJSON: localNodeMetadataJSON(rendered.FileType, "structure")}
		if err := tx.Create(&persisted).Error; err != nil {
			return err
		}
		node.leafPersistedID = persisted.ID
		for index, child := range node.Children {
			if err := persist(child, persisted.ID, index+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := persist(root, 0, 0); err != nil {
		return fmt.Errorf("persist content tree: %w", err)
	}
	return nil
}

func (dp *documentProcessor) storeGeneratedChunks(tx *gorm.DB, kbID, docID, revisionID int64, title string, rendered extractedDocument, units []generatedChunk) ([]model.DocumentChunk, error) {
	models := make([]model.DocumentChunk, len(units))
	for index, unit := range units {
		contextJSON, err := serializeStructureContext(title, revisionID, unit, rendered)
		if err != nil {
			return nil, fmt.Errorf("serialize chunk %d structure context: %w", index, err)
		}
		models[index] = model.DocumentChunk{KnowledgeBaseID: kbID, DocumentID: docID, RevisionID: revisionID, ChunkIndex: index, Content: unit.content, DisplayText: unit.content, SearchText: unit.searchText, StructureContextJSON: contextJSON, TokenCount: len(dp.splitter.tp.Encode(unit.searchText, nil, nil)), InputFingerprint: canonicalFingerprint("unit-v2", fmt.Sprintf("%d", revisionID), fmt.Sprintf("%d", index), unit.searchText), StartOffset: unit.start, EndOffset: unit.end}
	}
	if err := tx.Create(&models).Error; err != nil {
		return nil, fmt.Errorf("create generated chunks: %w", err)
	}
	return models, nil
}

// createProcessingRevision creates the immutable canonical record before any
// derived retrieval artifacts are written.
func (dp *documentProcessor) createProcessingRevision(doc *model.Document, text, adapterID string, profile retrievalTokenProfile) (*model.DocumentRevision, error) {
	contentHash := canonicalFingerprint("content-v1", text)
	parserID := fmt.Sprintf("local-%s", strings.TrimPrefix(doc.FileType, "."))
	parserVersion := fmt.Sprintf("%s|%s|tree-v1|generator-v1|context-v2|cl100k-base|%d:%d:%d:%d", canonicalParserVersion, adapterID, profile.MinTokens, profile.MaxTokens, profile.OverlapTokens, profile.EmbeddingMaxLen)
	fingerprint := canonicalFingerprint(contentHash, parserID, parserVersion, canonicalNormalizerVersion)
	revision := &model.DocumentRevision{
		DocumentID:           doc.ID,
		ContentHash:          contentHash,
		ParserID:             parserID,
		ParserVersion:        parserVersion,
		NormalizerVersion:    canonicalNormalizerVersion,
		CanonicalFingerprint: fingerprint,
		BlobPath:             doc.FilePath,
		Status:               model.DocumentRevisionStatusProcessing,
	}
	if err := database.DB.Create(revision).Error; err != nil {
		return nil, fmt.Errorf("create document revision: %w", err)
	}
	return revision, nil
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
			texts[j-i] = chunks[j].SearchText
		}

		embeddings, err := dp.embService.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("embedding batch %d failed: %w", i/embedBatchSize, err)
		}
		allEmbeddings = append(allEmbeddings, embeddings...)
	}

	return allEmbeddings, nil
}

// updateRevisionStatus records a revision failure without touching the active
// document pointer, allowing a previously active revision to remain visible.
func (dp *documentProcessor) updateRevisionStatus(revisionID int64, status int, errMsg string) {
	if err := database.DB.Model(&model.DocumentRevision{}).Where("id = ?", revisionID).Updates(map[string]interface{}{
		"status":        status,
		"error_message": errMsg,
	}).Error; err != nil {
		applogger.Error("failed to update document revision status", "revision_id", revisionID, "status", status, "error", err)
	}
}

// updateRevisionProvenance persists source-location verification independently
// from the broader processing state, so startup can inspect only revisions
// whose provenance has never been verified.
func (dp *documentProcessor) updateRevisionProvenance(revisionID int64, status int, errMsg string) error {
	if err := database.DB.Model(&model.DocumentRevision{}).Where("id = ?", revisionID).Updates(map[string]interface{}{
		"provenance_status": status,
		"provenance_error":  errMsg,
	}).Error; err != nil {
		return fmt.Errorf("update revision %d provenance status: %w", revisionID, err)
	}
	return nil
}

func canonicalFingerprint(parts ...string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%q", parts)))
	return fmt.Sprintf("sha256:v1:%x", sum[:])
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

// cleanupRevisionArtifacts removes only failed revision artifacts. It never
// touches an older active revision, preserving retrieval availability when a
// future replacement pipeline fails.
func (dp *documentProcessor) cleanupRevisionArtifacts(docID, revisionID int64) {
	var chunkIDs []int64
	if err := database.DB.Model(&model.DocumentChunk{}).Where("document_id = ? AND revision_id = ?", docID, revisionID).Pluck("id", &chunkIDs).Error; err != nil {
		applogger.Error("failed to load revision chunks for cleanup", "doc_id", docID, "revision_id", revisionID, "error", err)
		return
	}
	if len(chunkIDs) > 0 {
		if err := database.DB.Where("chunk_id IN ?", chunkIDs).Delete(&model.DocumentChunkNode{}).Error; err != nil {
			applogger.Error("failed to delete revision chunk-node mappings", "doc_id", docID, "revision_id", revisionID, "error", err)
		}
		if err := database.DB.Where("id IN ?", chunkIDs).Delete(&model.DocumentChunk{}).Error; err != nil {
			applogger.Error("failed to delete failed revision chunks", "doc_id", docID, "revision_id", revisionID, "error", err)
		}
	}
	if err := database.DB.Where("revision_id = ?", revisionID).Delete(&model.ContentNode{}).Error; err != nil {
		applogger.Error("failed to delete failed revision content nodes", "doc_id", docID, "revision_id", revisionID, "error", err)
	}
}
