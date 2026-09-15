package kb

import (
	"context"
	"fmt"
	"strings"

	applogger "qingqiu-world-server/internal/logger"
)

const (
	// DefaultScanContextExpansionFactor bounds structural context around base
	// retrieval anchors. top_k limits anchors; expansion may add neighbours.
	DefaultScanContextExpansionFactor = 3
)

// ScanOptions controls deterministic evidence retrieval for scan_kb.
type ScanOptions struct {
	TopK                   int
	DocumentTitle          string
	ExpandContext          bool
	ExpandRelations        bool
	RelationDepth          int
	RelationFanout         int
	RelationEvidenceBudget int
}

// ScanResult is a deterministic evidence package. It contains no task-level
// reasoning plan, conclusions, or LLM-generated logic.
type ScanResult struct {
	Status          int
	Outcome         string
	Evidence        []Evidence
	RelatedEvidence []Evidence
	RelationPaths   []RelationPath
}

// ScanMultiKBEvidence retrieves active evidence from authorized KB IDs. It is
// intentionally a single retrieval step plus optional Context Expansion; Focus
// owns whether more scans are needed.
func ScanMultiKBEvidence(ctx context.Context, kbIDs []int64, query string, options ScanOptions) (*ScanResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("scan query is required")
	}
	if len(kbIDs) == 0 {
		return &ScanResult{
			Status:          ScanStatusInsufficientEvidence,
			Outcome:         "no authorized knowledge bases",
			Evidence:        []Evidence{},
			RelatedEvidence: []Evidence{},
		}, nil
	}
	if options.TopK <= 0 {
		options.TopK = DefaultSearchTopK
	}

	applogger.Debug("KB evidence scan started",
		"kb_ids", kbIDs,
		"query_fingerprint", QueryFingerprint(query),
		"document", options.DocumentTitle,
		"top_k", options.TopK,
		"expand_context", options.ExpandContext,
		"expand_relations", options.ExpandRelations,
	)

	anchors, err := SearchMultiKBFiltered(ctx, kbIDs, query, options.TopK, options.DocumentTitle)
	if err != nil {
		applogger.Error("KB evidence scan retrieval failed", "kb_ids", kbIDs, "query_fingerprint", QueryFingerprint(query), "document", options.DocumentTitle, "error", err)
		return nil, fmt.Errorf("scan retrieval: %w", err)
	}

	maxEvidence := options.TopK
	if options.ExpandContext {
		maxEvidence = options.TopK * DefaultScanContextExpansionFactor
	}
	evidence, err := expandResults(ctx, anchors, options.ExpandContext, maxEvidence)
	if err != nil {
		applogger.Error("KB evidence scan context expansion failed", "anchor_count", len(anchors), "query_fingerprint", QueryFingerprint(query), "error", err)
		return nil, fmt.Errorf("scan context expansion: %w", err)
	}
	relationPaths := make([]RelationPath, 0)
	if options.ExpandRelations && options.DocumentTitle == "" {
		relationEvidence, paths, err := expandRelationsFromEvidence(ctx, evidence, kbIDs, RelationExpansionOptions{
			Depth:          options.RelationDepth,
			Fanout:         options.RelationFanout,
			EvidenceBudget: options.RelationEvidenceBudget,
		})
		if err != nil {
			applogger.Error("KB evidence scan relation expansion failed", "seed_count", len(evidence), "query_fingerprint", QueryFingerprint(query), "error", err)
			return nil, fmt.Errorf("scan relation expansion: %w", err)
		}
		evidence = appendUniqueEvidence(evidence, relationEvidence)
		relationPaths = paths
	} else if options.ExpandRelations && options.DocumentTitle != "" {
		applogger.Debug("KB relation expansion skipped for document-scoped scan", "document", options.DocumentTitle, "query_fingerprint", QueryFingerprint(query))
	}

	status := ScanStatusPartial
	outcome := "retrieval completed with active evidence"
	if len(evidence) == 0 {
		status = ScanStatusInsufficientEvidence
		outcome = "no active evidence found"
	}
	applogger.Debug("KB evidence scan completed",
		"status", status,
		"anchor_count", len(anchors),
		"evidence_count", len(evidence),
		"relation_path_count", len(relationPaths),
		"query_fingerprint", QueryFingerprint(query),
	)
	return &ScanResult{
		Status:          status,
		Outcome:         outcome,
		Evidence:        append([]Evidence(nil), evidence...),
		RelatedEvidence: append([]Evidence(nil), evidence...),
		RelationPaths:   append([]RelationPath(nil), relationPaths...),
	}, nil
}

// appendUniqueEvidence appends relation-expanded evidence without allowing it
// to duplicate base retrieval or structural context chunks.
func appendUniqueEvidence(base []Evidence, additions []Evidence) []Evidence {
	if len(additions) == 0 {
		return base
	}
	seen := make(map[int64]struct{}, len(base)+len(additions))
	for _, item := range base {
		if item.RetrievalUnitID > 0 {
			seen[item.RetrievalUnitID] = struct{}{}
		}
	}
	out := append([]Evidence(nil), base...)
	for _, item := range additions {
		if item.RetrievalUnitID <= 0 {
			applogger.Warn("KB evidence scan skipped relation evidence without retrieval unit", "document_id", item.DocumentID, "revision_id", item.RevisionID)
			continue
		}
		if _, exists := seen[item.RetrievalUnitID]; exists {
			continue
		}
		seen[item.RetrievalUnitID] = struct{}{}
		out = append(out, item)
	}
	return out
}
