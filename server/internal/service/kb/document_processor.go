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
	"qingqiu-world-server/internal/service/llm"

	"gorm.io/gorm"
)

const (
	defaultchunkSize           = 500
	defaultchunkOverlap        = 50
	defaultMinchunkSize        = 100
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
// It returns the processing revision, which the caller activates only after
// both serving indexes have accepted the new retrieval units.
func (dp *documentProcessor) Process(ctx context.Context, kbID int64, doc *model.Document) (*model.DocumentRevision, error) {
	dp.updateStatus(doc.ID, model.DocumentStatusProcessing, "")

	rendered, err := ExtractDocument(doc.FilePath)
	if err != nil {
		dp.cleanupchunks(doc.ID)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Extraction failed: %v", err))
		return nil, err
	}
	text := rendered.Text

	revision, err := dp.createProcessingRevision(doc, text)
	if err != nil {
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Revision creation failed: %v", err))
		return nil, err
	}

	chunks := dp.splitter.Split(text)
	if len(chunks) == 0 {
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, "No text content extracted")
		dp.updateRevisionStatus(revision.ID, model.DocumentRevisionStatusFailed, "No text content extracted")
		return nil, fmt.Errorf("no text content extracted from document %d", doc.ID)
	}
	if err := validateChunkProvenance(text, chunks); err != nil {
		message := fmt.Sprintf("Source provenance validation failed: %v", err)
		applogger.Error("document parsing produced invalid source provenance", "document_id", doc.ID, "error", err)
		dp.updateRevisionProvenance(revision.ID, model.DocumentRevisionProvenanceRepairRequired, message)
		dp.updateRevisionStatus(revision.ID, model.DocumentRevisionStatusFailed, message)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, message)
		return nil, err
	}

	chunkModels, err := dp.storechunks(kbID, doc.ID, revision.ID, doc.Title, chunks)
	if err != nil {
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Chunk storage failed: %v", err))
		dp.updateRevisionStatus(revision.ID, model.DocumentRevisionStatusFailed, fmt.Sprintf("Chunk storage failed: %v", err))
		return nil, err
	}
	if err := dp.storeContentNodes(doc.ID, revision.ID, doc.Title, rendered, chunkModels); err != nil {
		dp.cleanupRevisionArtifacts(doc.ID, revision.ID)
		dp.updateStatus(doc.ID, model.DocumentStatusFailed, fmt.Sprintf("Content node storage failed: %v", err))
		dp.updateRevisionStatus(revision.ID, model.DocumentRevisionStatusFailed, fmt.Sprintf("Content node storage failed: %v", err))
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

// storechunks persists a complete retrieval-unit batch. A database write
// failure must stop processing so no zero-ID chunks reach vector storage.
func (dp *documentProcessor) storechunks(kbID, docID, revisionID int64, title string, chunks []chunk) ([]model.DocumentChunk, error) {
	models := make([]model.DocumentChunk, len(chunks))
	for i, c := range chunks {
		searchText := fmt.Sprintf("Document: %s\n\n%s", title, c.Content)
		models[i] = model.DocumentChunk{
			KnowledgeBaseID:  kbID,
			DocumentID:       docID,
			RevisionID:       revisionID,
			ChunkIndex:       c.chunkIndex,
			Content:          c.Content,
			SearchText:       searchText,
			DisplayText:      c.Content,
			TokenCount:       len(tokenize(searchText)),
			InputFingerprint: canonicalFingerprint("unit-v1", fmt.Sprintf("%d", revisionID), fmt.Sprintf("%d", c.chunkIndex), searchText),
			StartOffset:      c.StartOffset,
			EndOffset:        c.EndOffset,
		}
	}
	if err := database.DB.Create(&models).Error; err != nil {
		applogger.Error("failed to create document chunks", "doc_id", docID, "count", len(models), "error", err)
		return nil, fmt.Errorf("create document chunks: %w", err)
	}
	return models, nil
}

// createProcessingRevision creates the immutable canonical record before any
// derived retrieval artifacts are written.
func (dp *documentProcessor) createProcessingRevision(doc *model.Document, text string) (*model.DocumentRevision, error) {
	contentHash := canonicalFingerprint("content-v1", text)
	parserID := fmt.Sprintf("local-%s", doc.FileType)
	fingerprint := canonicalFingerprint(contentHash, parserID, canonicalParserVersion, canonicalNormalizerVersion)
	revision := &model.DocumentRevision{
		DocumentID:           doc.ID,
		ContentHash:          contentHash,
		ParserID:             parserID,
		ParserVersion:        canonicalParserVersion,
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

// storeContentNodes persists the canonical local-upload tree. Every node has a
// source locator so retrieval provenance never degrades to an opaque {} value.
func (dp *documentProcessor) storeContentNodes(docID, revisionID int64, title string, rendered extractedDocument, chunks []model.DocumentChunk) error {
	root := model.ContentNode{
		DocumentID:   docID,
		RevisionID:   revisionID,
		ParentID:     0,
		Ordinal:      0,
		NodeType:     model.ContentNodeTypeDocument,
		Text:         title,
		ContentHash:  canonicalFingerprint("node-v1", title),
		LocatorJSON:  rendered.locatorJSON(-1, 0, len(rendered.Text)),
		MetadataJSON: localNodeMetadataJSON(rendered.FileType, "document"),
	}
	if err := database.DB.Create(&root).Error; err != nil {
		return fmt.Errorf("create root content node: %w", err)
	}

	parentID := root.ID
	parentOrdinal := 0
	for ordinal, chunk := range chunks {
		nodeType := contentNodeTypeForChunk(chunk.DisplayText)
		if nodeType == model.ContentNodeTypeHeading {
			// A heading is a structural parent, not a second retrieval unit. The
			// mapped chunk below remains the only evidence body and chunk ID.
			heading := model.ContentNode{
				DocumentID:   docID,
				RevisionID:   revisionID,
				ParentID:     root.ID,
				Ordinal:      ordinal + 1,
				NodeType:     model.ContentNodeTypeHeading,
				Text:         chunk.DisplayText,
				ContentHash:  canonicalFingerprint("node-v1", "heading", chunk.DisplayText),
				LocatorJSON:  rendered.locatorJSON(chunk.ChunkIndex, chunk.StartOffset, chunk.EndOffset),
				MetadataJSON: localNodeMetadataJSON(rendered.FileType, "heading"),
			}
			if err := database.DB.Create(&heading).Error; err != nil {
				return fmt.Errorf("create heading content node: %w", err)
			}
			parentID = heading.ID
			parentOrdinal = 0
		}
		parentOrdinal++
		node := model.ContentNode{
			DocumentID:   docID,
			RevisionID:   revisionID,
			ParentID:     parentID,
			Ordinal:      parentOrdinal,
			NodeType:     nodeType,
			Text:         chunk.DisplayText,
			ContentHash:  canonicalFingerprint("node-v1", chunk.DisplayText),
			LocatorJSON:  rendered.locatorJSON(chunk.ChunkIndex, chunk.StartOffset, chunk.EndOffset),
			MetadataJSON: localNodeMetadataJSON(rendered.FileType, "chunk"),
		}
		if err := database.DB.Create(&node).Error; err != nil {
			return fmt.Errorf("create paragraph content node: %w", err)
		}
		mapping := model.DocumentChunkNode{ChunkID: chunk.ID, NodeID: node.ID, Ordinal: 0}
		if err := database.DB.Create(&mapping).Error; err != nil {
			return fmt.Errorf("create chunk-node mapping: %w", err)
		}
	}
	return nil
}

// contentNodeTypeForChunk exposes the splitter's structural classification to
// Context Expansion while preserving DocumentChunk as the sole retrieval unit.
func contentNodeTypeForChunk(content string) int {
	paragraph := textParagraph{content: content}
	paragraph.heading = isMarkdownHeading(content)
	paragraph.code = strings.HasPrefix(strings.TrimSpace(content), "```") || strings.HasPrefix(content, "    ")
	paragraph.list = isListBlock(content)
	paragraph.table = isTableBlock(content)
	switch {
	case paragraph.heading:
		return model.ContentNodeTypeHeading
	case paragraph.code:
		return model.ContentNodeTypeCode
	case paragraph.table:
		return model.ContentNodeTypeTable
	case paragraph.list:
		return model.ContentNodeTypeList
	default:
		return model.ContentNodeTypeParagraph
	}
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
