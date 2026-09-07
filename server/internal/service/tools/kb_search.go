package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/schema"
	"qingqiu-world-server/internal/service/kb"
)

const (
	// scanKBDefaultTopK is the default number of chunks returned by scan_kb.
	scanKBDefaultTopK = 5
	// scanKBMaxTopK bounds the per-call result size of scan_kb.
	scanKBMaxTopK = 20
)

// KBAuthorizer resolves the knowledge bases the caller is allowed to access.
// The kb cores stay person-free: wrapper packages bind the person identity
// into this closure (typically via AuthorizedKBsFor). It is invoked on every
// tool execution so grant changes take effect immediately.
type KBAuthorizer func() ([]model.KnowledgeBase, error)

// AuthorizedKBsFor returns the standard KBAuthorizer bound to the given
// person's granted knowledge-base inventory, loaded through dops. This is the
// one sanctioned bridge between the person-free kb cores and person identity;
// it also owns the load-failure log where person context is available.
func AuthorizedKBsFor(personID int64) KBAuthorizer {
	return func() ([]model.KnowledgeBase, error) {
		kbs, err := dops.ListAuthorizedKBs(personID)
		if err != nil {
			applogger.Error("kb tools: failed to load authorized KBs", "person_id", personID, "error", err)
		}
		return kbs, err
	}
}

// authorizedKBSet loads the authorized KB inventory through the authorizer and
// returns it plus an ID membership set. Call-time loading keeps the
// authorization enforcement in sync with grant changes made during a session.
func authorizedKBSet(authorize KBAuthorizer) ([]model.KnowledgeBase, map[int64]struct{}, error) {
	kbs, err := authorize()
	if err != nil {
		return nil, nil, err
	}
	set := make(map[int64]struct{}, len(kbs))
	for _, k := range kbs {
		set[k.ID] = struct{}{}
	}
	return kbs, set, nil
}

// authorizedKBNames renders the authorized KB inventory as a compact
// "id name" list used in tool error messages so the LLM can self-correct.
func authorizedKBNames(kbs []model.KnowledgeBase) string {
	parts := make([]string, 0, len(kbs))
	for _, k := range kbs {
		parts = append(parts, fmt.Sprintf("#%d %s", k.ID, k.Name))
	}
	return strings.Join(parts, ", ")
}

// Shared LLM-facing wording for the kb tools, exported as plain data so that
// wrapper packages (task/tools, privatespace/tools) stay single-sourced. The
// llm.FunctionDefinition assembly itself stays in the wrapper layers.
const (
	// ScanKBDescription is the short one-line description of scan_kb.
	ScanKBDescription = "Semantic search over your authorized knowledge bases"
	// ScanKBSchemaDescription is the detailed description passed to the LLM's
	// function calling for scan_kb.
	ScanKBSchemaDescription = "Search your authorized knowledge bases by semantic query. " +
		"Returns the most relevant text chunks with their document title, KB id and score. " +
		"Optionally restrict the search to one knowledge base (kb_id) and/or one document " +
		"within it (document). Use list_kb_documents to discover available KBs and documents first."
	// ListKBDocumentsDescription is the short one-line description of list_kb_documents.
	ListKBDocumentsDescription = "List documents of one of your authorized knowledge bases"
	// ListKBDocumentsSchemaDescription is the detailed description passed to the
	// LLM's function calling for list_kb_documents.
	ListKBDocumentsSchemaDescription = "List the documents of one of your authorized knowledge bases. " +
		"Returns each document's title, type, size, chunk count and status. " +
		"Use the returned title as the document filter of scan_kb."
)

// ScanKBParameters returns the LLM-facing JSON schema of scan_kb arguments as
// plain data. Wrappers embed it in their llm.FunctionDefinition.
func ScanKBParameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query text.",
			},
			"kb_id": map[string]interface{}{
				"type":        "integer",
				"description": "Restrict the search to this knowledge base ID. Omit to search all authorized KBs.",
			},
			"document": map[string]interface{}{
				"type":        "string",
				"description": "Restrict results to the document whose title matches this value (exact match first, substring fallback).",
			},
			"top_k": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of chunks to return. Default: 5, max: 20.",
				"default":     scanKBDefaultTopK,
			},
		},
		"required": []string{"query"},
	}
}

// ListKBDocumentsParameters returns the LLM-facing JSON schema of
// list_kb_documents arguments as plain data. Wrappers embed it in their
// llm.FunctionDefinition.
func ListKBDocumentsParameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"kb_id": map[string]interface{}{
				"type": "integer",
				"description": "Knowledge base ID to list documents for. " +
					"If unknown, discover it via scan_kb results (each hit carries kb_id) or your instructions.",
			},
		},
		"required": []string{"kb_id"},
	}
}

// ScanKBTool performs semantic search over an authorized knowledge-base
// inventory. Retrieval goes through the vector index only — documents are
// never exposed as raw filesystem paths. Optional metadata filters: KB ID and
// document title within it.
//
// Person-free core: the authorized inventory is injected via KBAuthorizer and
// resolved at call time. Domain-agnostic by package contract: no
// Tool/ToolName/Schema interfaces here; higher-level packages (task/tools,
// privatespace/tools) wrap this core with their own contracts.
type ScanKBTool struct {
	authorize     KBAuthorizer
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewScanKBTool creates a ScanKBTool core bound to the given authorizer.
func NewScanKBTool(authorize KBAuthorizer) *ScanKBTool {
	return &ScanKBTool{authorize: authorize}
}

// scanKBResult is one search hit returned to the agent.
type scanKBResult struct {
	KBID          int64   `json:"kb_id"`
	DocumentTitle string  `json:"document_title"`
	Content       string  `json:"content"`
	Score         float64 `json:"score"`
}

// scanKBResponse wraps the search hits with an optional hint for the agent.
type scanKBResponse struct {
	Results []scanKBResult `json:"results"`
	Note    string         `json:"note,omitempty"`
}

// Execute runs the semantic search with authorization enforcement.
func (s *ScanKBTool) Execute(args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		applogger.Warn("scan_kb: empty query rejected", "args", args)
		return "", fmt.Errorf("query is required")
	}

	topK := scanKBDefaultTopK
	if v, ok := args["top_k"].(float64); ok && int(v) > 0 {
		topK = int(v)
	}
	if topK > scanKBMaxTopK {
		topK = scanKBMaxTopK
	}

	var kbID int64
	if v, ok := args["kb_id"].(float64); ok {
		kbID = int64(v)
	}
	docTitle, _ := args["document"].(string)
	docTitle = strings.TrimSpace(docTitle)

	kbs, authorized, err := authorizedKBSet(s.authorize)
	if err != nil {
		return "", fmt.Errorf("failed to load authorized knowledge bases: %w", err)
	}
	if len(kbs) == 0 {
		applogger.Warn("scan_kb: no authorized knowledge bases")
		resp, _ := json.Marshal(scanKBResponse{Results: []scanKBResult{}, Note: "you have no authorized knowledge bases"})
		return string(resp), nil
	}

	ctx := context.Background()
	var results []schema.SearchResult
	if kbID != 0 {
		if _, ok := authorized[kbID]; !ok {
			applogger.Warn("scan_kb: unauthorized KB requested",
				"kb_id", kbID, "authorized", authorizedKBNames(kbs))
			return "", fmt.Errorf("knowledge base %d is not authorized to you; authorized KBs: %s",
				kbID, authorizedKBNames(kbs))
		}
		results, err = kb.SearchKBFiltered(ctx, kbID, query, topK, docTitle)
	} else {
		ids := make([]int64, 0, len(kbs))
		for _, k := range kbs {
			ids = append(ids, k.ID)
		}
		results, err = kb.SearchMultiKBFiltered(ctx, ids, query, topK, docTitle)
	}
	if err != nil {
		applogger.Error("scan_kb: search failed", "kb_id", kbID, "document", docTitle, "error", err)
		return "", fmt.Errorf("knowledge base search failed: %w", err)
	}

	// Ordering and size are guaranteed by the kb layer: the multi-KB path
	// (SearchMultiKBFiltered) ranks all per-KB hits globally by score and
	// truncates to topK; the single-KB path returns at most topK already
	// ordered by score. No re-ranking here (single implementation).

	hits := make([]scanKBResult, 0, len(results))
	for _, r := range results {
		hits = append(hits, scanKBResult{
			KBID:          r.KnowledgeBaseID,
			DocumentTitle: r.DocumentTitle,
			Content:       r.Content,
			Score:         r.Score,
		})
	}

	resp := scanKBResponse{Results: hits}
	if len(hits) == 0 {
		resp.Note = "no matching chunks; check the document filter name and try a different query"
	}
	out, _ := json.Marshal(resp)
	return string(out), nil
}

// ListKBDocumentsTool lists the documents of one of the authorized knowledge
// bases. It is the discovery step that feeds scan_kb's document filter.
//
// Person-free core: see ScanKBTool for the layering contract.
type ListKBDocumentsTool struct {
	authorize     KBAuthorizer
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewListKBDocumentsTool creates a ListKBDocumentsTool core bound to the given
// authorizer.
func NewListKBDocumentsTool(authorize KBAuthorizer) *ListKBDocumentsTool {
	return &ListKBDocumentsTool{authorize: authorize}
}

// listKBDocumentEntry is one document summary returned to the agent.
// file_type / file_size describe the document's source material.
type listKBDocumentEntry struct {
	Title      string `json:"title"`
	FileType   string `json:"file_type"`
	FileSize   int64  `json:"file_size"`
	ChunkCount int    `json:"chunk_count"`
	Status     string `json:"status"`
}

// listKBDocumentsResponse wraps the document list.
type listKBDocumentsResponse struct {
	KBID      int64                 `json:"kb_id"`
	KBName    string                `json:"kb_name"`
	Documents []listKBDocumentEntry `json:"documents"`
}

// documentStatusName renders a Document status constant as a readable word.
func documentStatusName(status int) string {
	switch status {
	case model.DocumentStatusPending:
		return "pending"
	case model.DocumentStatusProcessing:
		return "processing"
	case model.DocumentStatusReady:
		return "ready"
	case model.DocumentStatusFailed:
		return "failed"
	case model.DocumentStatusDeleted:
		return "deleted"
	default:
		return fmt.Sprintf("unknown_%d", status)
	}
}

// Execute lists the documents of the requested KB with authorization enforcement.
func (t *ListKBDocumentsTool) Execute(args map[string]interface{}) (string, error) {
	var kbID int64
	if v, ok := args["kb_id"].(float64); ok {
		kbID = int64(v)
	}
	if kbID == 0 {
		applogger.Warn("list_kb_documents: missing kb_id", "args", args)
		return "", fmt.Errorf("kb_id is required")
	}

	kbs, authorized, err := authorizedKBSet(t.authorize)
	if err != nil {
		return "", fmt.Errorf("failed to load authorized knowledge bases: %w", err)
	}
	if _, ok := authorized[kbID]; !ok {
		applogger.Warn("list_kb_documents: unauthorized KB requested",
			"kb_id", kbID, "authorized", authorizedKBNames(kbs))
		return "", fmt.Errorf("knowledge base %d is not authorized to you; authorized KBs: %s",
			kbID, authorizedKBNames(kbs))
	}

	var kbName string
	for _, k := range kbs {
		if k.ID == kbID {
			kbName = k.Name
			break
		}
	}

	documents, err := dops.ListDocumentsInKB(kbID)
	if err != nil {
		applogger.Error("list_kb_documents: failed to list documents", "kb_id", kbID, "error", err)
		return "", fmt.Errorf("failed to list documents of KB %d: %w", kbID, err)
	}

	// Soft-deleted documents are excluded: they are no longer part of the KB.
	entries := make([]listKBDocumentEntry, 0, len(documents))
	for _, d := range documents {
		if d.Status == model.DocumentStatusDeleted {
			continue
		}
		entries = append(entries, listKBDocumentEntry{
			Title:      d.Title,
			FileType:   d.FileType,
			FileSize:   d.FileSize,
			ChunkCount: d.ChunkCount,
			Status:     documentStatusName(d.Status),
		})
	}

	out, _ := json.Marshal(listKBDocumentsResponse{KBID: kbID, KBName: kbName, Documents: entries})
	return string(out), nil
}
