package kb

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
)

// locatorForChunk resolves the canonical locator mapped to a retrieval unit.
// It keeps anchor and context-expanded evidence on the same metadata path.
func locatorForChunk(chunkID int64) (string, error) {
	var mapping model.DocumentChunkNode
	if err := database.DB.Where("chunk_id = ?", chunkID).Order("ordinal ASC").First(&mapping).Error; err != nil {
		return fallbackChunkLocator(chunkID, sourceRenderingVersionCurrent, fmt.Errorf("load chunk-node mapping: %w", err))
	}
	var node model.ContentNode
	if err := database.DB.First(&node, mapping.NodeID).Error; err != nil {
		return fallbackChunkLocator(chunkID, sourceRenderingVersionCurrent, fmt.Errorf("load content node: %w", err))
	}
	if hasCompleteLocator(node.LocatorJSON) {
		return node.LocatorJSON, nil
	}
	renderingVersion, known := sourceRenderingVersionFromMetadata(node.MetadataJSON)
	if !known {
		renderingVersion = sourceRenderingVersionLegacy
	}
	return fallbackChunkLocator(chunkID, renderingVersion, fmt.Errorf("content node %d has incomplete locator", node.ID))
}

// fallbackChunkLocator reconstructs complete provenance from the local source
// when a legacy node has not yet been backfilled. Returning an error is safer
// than emitting a partial locator: callers must never expose broken metadata.
func fallbackChunkLocator(chunkID int64, renderingVersion int, cause error) (string, error) {
	var chunk model.DocumentChunk
	if err := database.DB.First(&chunk, chunkID).Error; err != nil {
		return "", fmt.Errorf("%w; load chunk: %v", cause, err)
	}
	var document model.Document
	if err := database.DB.Select("file_path, file_type").First(&document, chunk.DocumentID).Error; err != nil {
		return "", fmt.Errorf("%w; load document: %v", cause, err)
	}
	rendered, err := extractDocumentForRendering(document.FilePath, renderingVersion)
	if err != nil {
		return "", fmt.Errorf("%w; extract source for fallback locator: %v", cause, err)
	}
	locator := rendered.locatorJSON(chunk.ChunkIndex, chunk.StartOffset, chunk.EndOffset)
	if !hasCompleteLocator(locator) {
		return "", fmt.Errorf("%w; reconstructed locator is incomplete", cause)
	}
	applogger.Warn("KB evidence is using fallback chunk locator", "chunk_id", chunkID, "cause", cause)
	return locator, nil
}

// hasCompleteLocator rejects both legacy {} values and partially populated JSON
// because downstream evidence reads must receive all location fields together.
func hasCompleteLocator(locator string) bool {
	if locator == "" || locator == "{}" {
		return false
	}
	var decoded evidenceLocator
	if err := json.Unmarshal([]byte(locator), &decoded); err != nil {
		applogger.Warn("KB content node has invalid locator JSON", "locator", locator, "error", err)
		return false
	}
	return decoded.FileType != "" && decoded.ChunkIndex >= 0 && decoded.LineStart > 0 && decoded.LineEnd > 0
}

// BackfillContentNodeLocators verifies only legacy revisions whose provenance
// state is unknown. A verified revision is never rescanned at startup; a failed
// verification becomes repair-required and is left for an explicit re-upload.
func BackfillContentNodeLocators() {
	var revisions []model.DocumentRevision
	if err := database.DB.Table("document_revisions AS revisions").
		Select("revisions.*").
		Joins("JOIN documents AS documents ON documents.active_revision_id = revisions.id").
		Where("documents.status = ? AND revisions.provenance_status = ?", model.DocumentStatusReady, model.DocumentRevisionProvenanceUnknown).
		Find(&revisions).Error; err != nil {
		applogger.Error("KB provenance verification could not load unknown active revisions", "error", err)
		return
	}
	updatedNodes := 0
	verifiedRevisions := 0
	repairRequired := 0
	for _, revision := range revisions {
		var document model.Document
		if err := database.DB.Where("id = ? AND active_revision_id = ?", revision.DocumentID, revision.ID).First(&document).Error; err != nil {
			applogger.Error("KB provenance verification could not load active document", "document_id", revision.DocumentID, "revision_id", revision.ID, "error", err)
			markRevisionProvenanceRepairRequired(revision.ID, fmt.Sprintf("Active document lookup failed: %v", err))
			repairRequired++
			continue
		}
		if document.SourceKind != model.DocumentSourceKindLocalUpload {
			message := fmt.Sprintf("Unsupported source kind %d", document.SourceKind)
			applogger.Warn("KB provenance verification skipped unsupported source kind", "document_id", document.ID, "revision_id", revision.ID, "source_kind", document.SourceKind)
			markRevisionProvenanceRepairRequired(revision.ID, message)
			repairRequired++
			continue
		}
		renderingVersion, err := renderingVersionForRevision(document)
		if err != nil {
			applogger.Error("KB provenance verification could not inspect active content nodes", "document_id", document.ID, "revision_id", revision.ID, "error", err)
			markRevisionProvenanceRepairRequired(revision.ID, err.Error())
			repairRequired++
			continue
		}
		rendered, err := extractDocumentForRendering(document.FilePath, renderingVersion)
		if err != nil {
			applogger.Error("KB provenance verification could not extract local upload", "document_id", document.ID, "revision_id", revision.ID, "path", document.FilePath, "error", err)
			markRevisionProvenanceRepairRequired(revision.ID, fmt.Sprintf("Source extraction failed: %v", err))
			repairRequired++
			continue
		}
		count, err := backfillDocumentNodeLocators(document, rendered, renderingVersion)
		if err != nil {
			applogger.Error("KB provenance verification failed", "document_id", document.ID, "revision_id", revision.ID, "error", err)
			markRevisionProvenanceRepairRequired(revision.ID, err.Error())
			repairRequired++
			continue
		}
		if err := database.DB.Model(&model.DocumentRevision{}).Where("id = ?", revision.ID).Updates(map[string]interface{}{
			"provenance_status": model.DocumentRevisionProvenanceVerified,
			"provenance_error":  "",
		}).Error; err != nil {
			applogger.Error("KB provenance verification could not persist verified state", "document_id", document.ID, "revision_id", revision.ID, "error", err)
			continue
		}
		updatedNodes += count
		verifiedRevisions++
	}
	applogger.Info("KB provenance verification completed", "updated_node_count", updatedNodes, "verified_revision_count", verifiedRevisions, "repair_required_revision_count", repairRequired, "unknown_revision_count", len(revisions))
}

// renderingVersionForRevision chooses the source rendition that produced an
// unknown revision's chunks. Versionless metadata is necessarily legacy.
func renderingVersionForRevision(document model.Document) (int, error) {
	var nodes []model.ContentNode
	if err := database.DB.Where("document_id = ? AND revision_id = ?", document.ID, document.ActiveRevisionID).Find(&nodes).Error; err != nil {
		return 0, fmt.Errorf("load active content nodes: %w", err)
	}
	if len(nodes) == 0 {
		return 0, fmt.Errorf("active revision has no content nodes")
	}
	renderingVersion := sourceRenderingVersionCurrent
	for _, node := range nodes {
		version, known := sourceRenderingVersionFromMetadata(node.MetadataJSON)
		if !known {
			// Every versionless node was created by the former extractor, so
			// the entire revision must use its exact historical rendition.
			renderingVersion = sourceRenderingVersionLegacy
			continue
		}
		if version == sourceRenderingVersionLegacy {
			renderingVersion = sourceRenderingVersionLegacy
		}
	}
	return renderingVersion, nil
}

// markRevisionProvenanceRepairRequired ends automatic retries for a revision
// whose source cannot be verified. The stored reason makes explicit re-upload
// or repair diagnosable without another full startup scan.
func markRevisionProvenanceRepairRequired(revisionID int64, message string) {
	if err := database.DB.Model(&model.DocumentRevision{}).Where("id = ?", revisionID).Updates(map[string]interface{}{
		"provenance_status": model.DocumentRevisionProvenanceRepairRequired,
		"provenance_error":  message,
	}).Error; err != nil {
		applogger.Error("KB provenance verification could not persist repair-required state", "revision_id", revisionID, "error", err)
	}
}

func backfillDocumentNodeLocators(document model.Document, rendered extractedDocument, renderingVersion int) (int, error) {
	var chunks []model.DocumentChunk
	if err := database.DB.Where("document_id = ? AND revision_id = ? AND deleted = 0", document.ID, document.ActiveRevisionID).Find(&chunks).Error; err != nil {
		return 0, fmt.Errorf("load active chunks: %w", err)
	}
	alignedChunks, err := alignPersistedChunkOffsets(document, rendered, chunks)
	if err != nil {
		return 0, err
	}
	if len(alignedChunks) != len(chunks) {
		return 0, fmt.Errorf("source alignment failed for %d of %d active chunks", len(chunks)-len(alignedChunks), len(chunks))
	}
	chunkByID := make(map[int64]model.DocumentChunk, len(chunks))
	for _, chunk := range chunks {
		chunkByID[chunk.ID] = chunk
	}
	var nodes []model.ContentNode
	if err := database.DB.Where("document_id = ? AND revision_id = ?", document.ID, document.ActiveRevisionID).Find(&nodes).Error; err != nil {
		return 0, fmt.Errorf("load content nodes: %w", err)
	}
	var mappings []model.DocumentChunkNode
	if err := database.DB.Where("chunk_id IN ?", chunkIDsForLocatorBackfill(chunks)).Find(&mappings).Error; err != nil {
		return 0, fmt.Errorf("load chunk-node mappings: %w", err)
	}
	chunkByNodeID := make(map[int64]model.DocumentChunk, len(mappings))
	for _, mapping := range mappings {
		if chunk, exists := chunkByID[mapping.ChunkID]; exists {
			chunkByNodeID[mapping.NodeID] = chunk
		}
	}

	updates := 0
	for _, node := range nodes {
		locator := ""
		metadata := ""
		if node.ParentID == 0 {
			locator = rendered.locatorJSON(-1, 0, len(rendered.Text))
			metadata = localNodeMetadataJSONForRendering(rendered.FileType, "document", renderingVersion)
		} else if chunk, exists := chunkByNodeID[node.ID]; exists {
			if !alignedChunks[chunk.ID] {
				// Do not replace a locator with coordinates derived from an
				// unverified legacy offset. The warning emitted by alignment
				// keeps this exceptional source visible to operators.
				continue
			}
			locator = rendered.locatorJSON(chunk.ChunkIndex, chunk.StartOffset, chunk.EndOffset)
			metadata = localNodeMetadataJSONForRendering(rendered.FileType, "chunk", renderingVersion)
		} else {
			return updates, fmt.Errorf("unmapped content node %d in active revision", node.ID)
		}
		if node.LocatorJSON == locator && node.MetadataJSON == metadata {
			continue
		}
		if err := database.DB.Model(&model.ContentNode{}).Where("id = ?", node.ID).Updates(map[string]interface{}{
			"locator_json":  locator,
			"metadata_json": metadata,
		}).Error; err != nil {
			return updates, fmt.Errorf("update node %d locator: %w", node.ID, err)
		}
		updates++
	}
	return updates, nil
}

// alignPersistedChunkOffsets reconciles legacy offsets with the current
// canonical rendition before emitting locator metadata. Legacy splitters may
// have collapsed line breaks into spaces, so matching treats all whitespace
// runs as equivalent while preserving the exact source byte range. It changes
// only source positions, never chunk IDs, content, vectors, or retrieval order.
func alignPersistedChunkOffsets(document model.Document, rendered extractedDocument, chunks []model.DocumentChunk) (map[int64]bool, error) {
	aligned := make(map[int64]bool, len(chunks))
	sort.Slice(chunks, func(i, j int) bool {
		if chunks[i].ChunkIndex == chunks[j].ChunkIndex {
			return chunks[i].ID < chunks[j].ID
		}
		return chunks[i].ChunkIndex < chunks[j].ChunkIndex
	})
	searchText := normalizeLocatorSearchText(rendered.Text)
	searchStart := 0
	for index := range chunks {
		content := chunks[index].DisplayText
		if content == "" {
			content = chunks[index].Content
		}
		if content == "" {
			applogger.Warn("KB locator backfill skipped empty chunk content", "document_id", document.ID, "chunk_id", chunks[index].ID)
			continue
		}
		start, end, found := searchText.find(content, searchStart)
		if !found {
			applogger.Warn("KB locator backfill could not align persisted chunk", "document_id", document.ID, "chunk_id", chunks[index].ID, "chunk_index", chunks[index].ChunkIndex, "content_bytes", len(content))
			continue
		}
		if chunks[index].StartOffset != start || chunks[index].EndOffset != end {
			if err := database.DB.Model(&model.DocumentChunk{}).Where("id = ?", chunks[index].ID).Updates(map[string]interface{}{
				"start_offset": start,
				"end_offset":   end,
			}).Error; err != nil {
				return aligned, fmt.Errorf("update chunk %d offsets: %w", chunks[index].ID, err)
			}
			chunks[index].StartOffset = start
			chunks[index].EndOffset = end
		}
		aligned[chunks[index].ID] = true
		// Search from the current start, rather than end, because chunks can
		// intentionally overlap while retaining independently valid locators.
		searchStart = start
	}
	return aligned, nil
}

// normalizedLocatorText maps a whitespace-normalized rendition back to exact
// byte offsets in the original extracted source. It is used only to migrate
// legacy chunks; newly created chunks already carry exact offsets.
type normalizedLocatorText struct {
	text  string
	start []int
	end   []int
}

func normalizeLocatorSearchText(text string) normalizedLocatorText {
	var builder strings.Builder
	starts := make([]int, 0, len(text))
	ends := make([]int, 0, len(text))
	for offset := 0; offset < len(text); {
		r, size := utf8.DecodeRuneInString(text[offset:])
		if unicode.IsSpace(r) {
			whitespaceStart := offset
			offset += size
			for offset < len(text) {
				next, nextSize := utf8.DecodeRuneInString(text[offset:])
				if !unicode.IsSpace(next) {
					break
				}
				offset += nextSize
			}
			builder.WriteByte(' ')
			starts = append(starts, whitespaceStart)
			ends = append(ends, offset)
			continue
		}
		builder.WriteString(text[offset : offset+size])
		for byteIndex := 0; byteIndex < size; byteIndex++ {
			starts = append(starts, offset)
			ends = append(ends, offset+size)
		}
		offset += size
	}
	return normalizedLocatorText{text: builder.String(), start: starts, end: ends}
}

// find returns the exact original range for a legacy chunk. It searches from
// the previous hit first, then the whole source to support intentional overlap.
func (text normalizedLocatorText) find(content string, sourceStart int) (int, int, bool) {
	needle := normalizeLocatorSearchText(content).text
	if needle == "" || len(text.text) == 0 {
		return 0, 0, false
	}
	startAt := sort.Search(len(text.end), func(index int) bool {
		return text.end[index] > sourceStart
	})
	position := strings.Index(text.text[startAt:], needle)
	if position < 0 {
		position = strings.Index(text.text, needle)
	} else {
		position += startAt
	}
	if position < 0 || position+len(needle) > len(text.start) {
		return 0, 0, false
	}
	return text.start[position], text.end[position+len(needle)-1], true
}

func chunkIDsForLocatorBackfill(chunks []model.DocumentChunk) []int64 {
	ids := make([]int64, 0, len(chunks))
	for _, chunk := range chunks {
		ids = append(ids, chunk.ID)
	}
	return ids
}
