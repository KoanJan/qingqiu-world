package kb

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"unicode/utf8"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
)

const (
	// estimatedCharactersPerToken is deliberately a simple, conservative
	// character-length conversion. BM25 terms are retrieval features, not model
	// tokens, and must never be used to enforce LLM context budgets.
	estimatedCharactersPerToken = 2

	// ScanStatusComplete is reserved for future deterministic scan adapters
	// that can prove a bounded task is complete. scan_kb normally returns
	// partial evidence because retrieval is not exhaustive.
	ScanStatusComplete = 0
	// ScanStatusPartial means retrieval produced useful but incomplete evidence.
	ScanStatusPartial = 1
	// ScanStatusInsufficientEvidence means no active evidence was found.
	ScanStatusInsufficientEvidence = 2
)

// EvidenceExpansionKind identifies why an evidence item entered a retrieval
// package. It is provenance, not relevance scoring.
type EvidenceExpansionKind int

const (
	// EvidenceExpansionKindAnchor means the retriever returned the chunk
	// directly for the user's query.
	EvidenceExpansionKindAnchor EvidenceExpansionKind = 0
	// EvidenceExpansionKindStructuralContext means the chunk was added because
	// it is adjacent to an anchor in the document structure.
	EvidenceExpansionKindStructuralContext EvidenceExpansionKind = 1
	// EvidenceExpansionKindRelation means the chunk was added through an
	// evidence-grounded KB relation. The relation is a retrieval hint only.
	EvidenceExpansionKindRelation EvidenceExpansionKind = 2
)

// Evidence preserves the active revision provenance required by Context
// Expansion, Relation Expansion, scan_kb and read_kb_evidence.
type Evidence struct {
	KnowledgeBaseID int64                 `json:"knowledge_base_id"`
	DocumentID      int64                 `json:"document_id"`
	DocumentTitle   string                `json:"document_title"`
	RevisionID      int64                 `json:"revision_id"`
	RetrievalUnitID int64                 `json:"retrieval_unit_id"`
	Content         string                `json:"content"`
	LocatorJSON     string                `json:"locator_json"`
	Score           float64               `json:"score"`
	ExpansionKind   EvidenceExpansionKind `json:"expansion_kind"`
}

// QueryFingerprint returns a stable non-reversible identifier for logging
// query-like text without copying the user's raw prompt or KB content.
func QueryFingerprint(query string) string {
	sum := sha256.Sum256([]byte(query))
	return fmt.Sprintf("sha256:v1:%x", sum[:])
}

func expandResults(ctx context.Context, anchors []schema.SearchResult, expand bool, maxItems int) ([]Evidence, error) {
	// Context is expanded only through the canonical node mapping. This keeps
	// adjacent text inside the same document/revision structure and makes every
	// added unit traceable to a ContentNode locator.
	if maxItems <= 0 || len(anchors) == 0 {
		return make([]Evidence, 0), nil
	}

	// Preserve the retriever's stable rank order. In particular, do not sort
	// neighbours together with anchors: an adjacent chunk inherits its anchor's
	// score and must not win a tie merely because it has a smaller database ID.
	items := make([]Evidence, 0, maxItems)
	seen := make(map[int64]struct{}, len(anchors)*3)
	selectedAnchors := make([]schema.SearchResult, 0, minInt(len(anchors), maxItems))
	for _, anchor := range anchors {
		if len(selectedAnchors) >= maxItems {
			break
		}
		if _, exists := seen[anchor.ChunkID]; exists {
			continue
		}
		anchorEvidence, err := evidenceFromResult(anchor, EvidenceExpansionKindAnchor)
		if err != nil {
			return nil, err
		}
		items = append(items, anchorEvidence)
		seen[anchor.ChunkID] = struct{}{}
		selectedAnchors = append(selectedAnchors, anchor)
	}
	if !expand || len(items) == maxItems {
		for _, anchor := range selectedAnchors {
			if !expand {
				applogger.Debug("KB context expansion disabled for anchor", "chunk_id", anchor.ChunkID, "document_id", anchor.DocumentID, "revision_id", anchor.RevisionID)
			}
		}
		return items, nil
	}

	// Store each anchor's structural context separately, then append it only
	// after all anchors have been retained. This is the invariant that protects
	// raw retrieval results from context-expansion starvation.
	neighbours := make([][]Evidence, len(selectedAnchors))
	for anchorIndex, anchor := range selectedAnchors {
		var document model.Document
		if err := database.DB.Select("active_revision_id, status").Where("id = ?", anchor.DocumentID).First(&document).Error; err != nil {
			return nil, err
		}
		if document.Status != model.DocumentStatusReady || document.ActiveRevisionID != anchor.RevisionID {
			applogger.Warn("KB context expansion skipped superseded revision", "document_id", anchor.DocumentID, "anchor_revision_id", anchor.RevisionID, "active_revision_id", document.ActiveRevisionID, "status", document.Status)
			continue
		}
		var mappings []model.DocumentChunkNode
		if err := database.DB.Where("chunk_id = ?", anchor.ChunkID).Find(&mappings).Error; err != nil {
			return nil, err
		}
		if len(mappings) == 0 {
			applogger.Warn("KB context expansion skipped unmapped retrieval unit", "chunk_id", anchor.ChunkID, "document_id", anchor.DocumentID, "revision_id", anchor.RevisionID)
			continue
		}
		for _, mapping := range mappings {
			var node model.ContentNode
			if err := database.DB.Where("id = ? AND document_id = ? AND revision_id = ?", mapping.NodeID, anchor.DocumentID, anchor.RevisionID).First(&node).Error; err != nil {
				return nil, err
			}
			var siblingNodes []model.ContentNode
			if err := database.DB.Where("document_id = ? AND revision_id = ? AND parent_id = ? AND ordinal BETWEEN ? AND ?", anchor.DocumentID, anchor.RevisionID, node.ParentID, node.Ordinal-2, node.Ordinal+2).Order("ordinal ASC").Find(&siblingNodes).Error; err != nil {
				return nil, err
			}
			if len(siblingNodes) == 0 {
				continue
			}
			nodeIDs := make([]int64, 0, len(siblingNodes))
			locators := make(map[int64]string, len(siblingNodes))
			nodeOrdinals := make(map[int64]int, len(siblingNodes))
			for _, siblingNode := range siblingNodes {
				nodeIDs = append(nodeIDs, siblingNode.ID)
				locators[siblingNode.ID] = siblingNode.LocatorJSON
				nodeOrdinals[siblingNode.ID] = siblingNode.Ordinal
			}
			var siblingMappings []model.DocumentChunkNode
			if err := database.DB.Where("node_id IN ?", nodeIDs).Find(&siblingMappings).Error; err != nil {
				return nil, err
			}
			sort.Slice(siblingMappings, func(i, j int) bool {
				if nodeOrdinals[siblingMappings[i].NodeID] != nodeOrdinals[siblingMappings[j].NodeID] {
					return nodeOrdinals[siblingMappings[i].NodeID] < nodeOrdinals[siblingMappings[j].NodeID]
				}
				return siblingMappings[i].ChunkID < siblingMappings[j].ChunkID
			})
			for _, siblingMapping := range siblingMappings {
				if _, exists := seen[siblingMapping.ChunkID]; exists {
					continue
				}
				var sibling model.DocumentChunk
				if err := database.DB.Where("id = ? AND document_id = ? AND revision_id = ? AND deleted = 0", siblingMapping.ChunkID, anchor.DocumentID, anchor.RevisionID).First(&sibling).Error; err != nil {
					return nil, err
				}
				content := sibling.DisplayText
				if content == "" {
					content = sibling.Content
				}
				locator := locators[siblingMapping.NodeID]
				if !hasCompleteLocator(locator) {
					resolvedLocator, err := locatorForChunk(sibling.ID)
					if err != nil {
						return nil, fmt.Errorf("resolve context-expanded locator for chunk %d: %w", sibling.ID, err)
					}
					locator = resolvedLocator
				}
				if !hasCompleteLocator(locator) {
					return nil, fmt.Errorf("context-expanded chunk %d has incomplete locator", sibling.ID)
				}
				seen[sibling.ID] = struct{}{}
				neighbours[anchorIndex] = append(neighbours[anchorIndex], Evidence{KnowledgeBaseID: anchor.KnowledgeBaseID, DocumentID: sibling.DocumentID, DocumentTitle: anchor.DocumentTitle, RevisionID: sibling.RevisionID, RetrievalUnitID: sibling.ID, Content: content, LocatorJSON: locator, Score: anchor.Score, ExpansionKind: EvidenceExpansionKindStructuralContext})
			}
		}
		applogger.Debug("KB context expansion completed for anchor", "chunk_id", anchor.ChunkID, "document_id", anchor.DocumentID, "revision_id", anchor.RevisionID, "neighbour_count", len(neighbours[anchorIndex]))
	}
	return appendStructuralNeighbours(items, neighbours, maxItems), nil
}

// appendStructuralNeighbours fills only capacity left after every selected
// anchor. Keeping this pure makes the anchor-retention invariant testable
// without a database-backed context-expansion fixture.
func appendStructuralNeighbours(anchors []Evidence, neighbours [][]Evidence, maxItems int) []Evidence {
	items := append([]Evidence(nil), anchors...)
	for _, group := range neighbours {
		for _, item := range group {
			if len(items) >= maxItems {
				return items
			}
			items = append(items, item)
		}
	}
	return items
}

// minInt returns the smaller non-negative capacity used for bounded slices.
func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func evidenceFromResult(result schema.SearchResult, kind EvidenceExpansionKind) (Evidence, error) {
	locator, err := locatorForChunk(result.ChunkID)
	if err != nil {
		return Evidence{}, fmt.Errorf("resolve retrieval locator for chunk %d: %w", result.ChunkID, err)
	}
	if !hasCompleteLocator(locator) {
		return Evidence{}, fmt.Errorf("retrieval chunk %d has incomplete locator", result.ChunkID)
	}
	return Evidence{KnowledgeBaseID: result.KnowledgeBaseID, DocumentID: result.DocumentID, DocumentTitle: result.DocumentTitle, RevisionID: result.RevisionID, RetrievalUnitID: result.ChunkID, Content: result.Content, LocatorJSON: locator, Score: result.Score, ExpansionKind: kind}, nil
}

// EstimateTextTokens converts Unicode character length to a bounded, rough
// model-token estimate. It intentionally does not reuse BM25 segmentation:
// retrieval tokenization and model-context accounting have different semantics.
func EstimateTextTokens(text string) int {
	runeCount := utf8.RuneCountInString(text)
	if runeCount == 0 {
		return 0
	}
	return (runeCount + estimatedCharactersPerToken - 1) / estimatedCharactersPerToken
}

// TruncateEvidenceContent applies a character-derived token budget only to
// evidence bodies. The caller keeps all provenance metadata even when an
// evidence body is shortened or omitted, so a later direct evidence read can
// always recover it.
func TruncateEvidenceContent(evidence []Evidence, tokenBudget int) ([]Evidence, bool, int) {
	if tokenBudget < 0 {
		tokenBudget = 0
	}
	trimmed := make([]Evidence, 0, len(evidence))
	usedTokens := 0
	truncated := false
	for _, item := range evidence {
		copyItem := item
		remaining := tokenBudget - usedTokens
		itemTokens := EstimateTextTokens(copyItem.Content)
		if itemTokens > remaining {
			copyItem.Content = truncateTextByTokens(copyItem.Content, remaining)
			truncated = true
			itemTokens = EstimateTextTokens(copyItem.Content)
		}
		usedTokens += itemTokens
		trimmed = append(trimmed, copyItem)
	}
	return trimmed, truncated, usedTokens
}

// truncateTextByTokens returns the longest rune prefix that stays within the
// supplied token budget. It deliberately operates on original text rather than
// reconstructed tokens, preserving text exactly up to the truncation point.
func truncateTextByTokens(text string, tokenBudget int) string {
	if tokenBudget <= 0 || text == "" {
		return ""
	}
	if EstimateTextTokens(text) <= tokenBudget {
		return text
	}
	runes := []rune(text)
	low, high := 0, len(runes)
	for low < high {
		mid := low + (high-low+1)/2
		if EstimateTextTokens(string(runes[:mid])) <= tokenBudget {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return string(runes[:low])
}
