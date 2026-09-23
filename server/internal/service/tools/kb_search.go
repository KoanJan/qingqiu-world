package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/kb"
)

const (
	// scanKBDefaultTopK is the default per-hop retrieval candidate count.
	scanKBDefaultTopK = 5
	// scanKBMaxTopK bounds each retrieval hop, not the structured result.
	scanKBMaxTopK = 20
	// scanKBEvidenceTokenBudget caps only evidence content in scan_kb output.
	// Provenance metadata is intentionally never truncated.
	scanKBEvidenceTokenBudget = 4000
	// readKBEvidenceMaxTokens prevents one direct evidence request from creating
	// an unbounded task context while still allowing progressive disclosure.
	readKBEvidenceMaxTokens = 16000
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
// wrapper packages (focusedwork/tools, privatespace/tools) stay single-sourced. The
// llm.FunctionDefinition assembly itself stays in the wrapper layers.
const (
	// ScanKBDescription is the short one-line description of scan_kb.
	ScanKBDescription = "Semantic search over authorized knowledge bases; results are not exhaustive"
	// ScanKBSchemaDescription is the detailed description passed to the LLM's
	// function calling for scan_kb.
	ScanKBSchemaDescription = "Search your authorized knowledge bases by semantic query. This is retrieval, not a full-document or full-KB traversal: returned evidence is a subset. " +
		"Never claim complete coverage, all contents, or facts outside returned evidence. For an inventory, use list_kb_documents; describe findings as based on retrieved evidence. " +
		"Provide a short reason describing the knowledge gap this query is meant to resolve; it is stored as query-intent trace, not as evidence or hidden reasoning. " +
		"top_k limits base retrieval candidates, not final related evidence metadata. Returns complete related document/chunk metadata and evidence content for the workload runtime to reason over. " +
		"Semantic relation paths may expand retrieval to additional evidence chunks; relation paths are provenance hints, not evidence or conclusions, and may include natural-language applicability notes that limit the relation proposition. " +
		"Only evidence content can be token-truncated; use read_kb_evidence with returned chunk IDs to read any full evidence body. " +
		"Optionally restrict the search to one knowledge base (kb_id) and/or one document " +
		"within it (document). Use list_kb_documents to discover available KBs and documents first."
	// ReadKBEvidenceDescription is the short description of direct evidence reads.
	ReadKBEvidenceDescription = "Read full evidence content by authorized chunk ID"
	// ReadKBEvidenceSchemaDescription is the detailed direct-evidence contract.
	ReadKBEvidenceSchemaDescription = "Read evidence bodies by chunk ID returned from scan_kb. " +
		"Each ID is rechecked against your current KB authorization and the document's active revision. " +
		"Only evidence content can be token-truncated; all requested evidence metadata is retained."
	// ListKBDocumentsDescription is the short one-line description of list_kb_documents.
	ListKBDocumentsDescription = "List documents of one of your authorized knowledge bases"
	// ListKBDocumentsSchemaDescription is the detailed description passed to the
	// LLM's function calling for list_kb_documents.
	ListKBDocumentsSchemaDescription = "List the document inventory of one authorized knowledge base. " +
		"Returns each document's title, type, size, chunk count and status, but not its body or an exhaustive evidence-ID list. " +
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
			"reason": map[string]interface{}{
				"type":        "string",
				"description": "Short query intent: what knowledge gap this scan is trying to fill. This is not chain-of-thought and is not treated as KB evidence.",
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
				"description": "Candidates to retrieve in each hop. Default: 5, max: 20. It does not limit final related evidence metadata.",
				"default":     scanKBDefaultTopK,
			},
		},
		"required": []string{"query", "reason"},
	}
}

// ReadKBEvidenceParameters returns the LLM-facing JSON schema for direct
// progressive disclosure of evidence returned by scan_kb.
func ReadKBEvidenceParameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"chunk_ids": map[string]interface{}{
				"type":        "array",
				"description": "Chunk IDs from related_evidence or evidence in a prior scan_kb result.",
				"items":       map[string]interface{}{"type": "integer"},
			},
			"max_tokens": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum total evidence-content tokens. Default: 4000, max: 16000. Metadata is never truncated.",
				"default":     scanKBEvidenceTokenBudget,
			},
		},
		"required": []string{"chunk_ids"},
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
// Tool/ToolName/Schema interfaces here; higher-level packages (focusedwork/tools,
// privatespace/tools) wrap this core with their own contracts.
type ScanKBTool struct {
	authorize     KBAuthorizer
	traceContext  ScanKBTraceContext
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// ScanKBTraceContext identifies the workload/session that issued scan_kb. A
// zero value is allowed for non-workload callers, such as private-space tools.
type ScanKBTraceContext struct {
	WorkID    int64
	SessionID int64
}

// NewScanKBTool creates a ScanKBTool core bound to the given authorizer.
func NewScanKBTool(authorize KBAuthorizer) *ScanKBTool {
	return &ScanKBTool{authorize: authorize}
}

// WithTraceContext returns the tool after binding runtime trace identifiers.
func (s *ScanKBTool) WithTraceContext(context ScanKBTraceContext) *ScanKBTool {
	s.traceContext = context
	return s
}

// scanKBConclusion is kept for response-shape compatibility. Phase A leaves
// it empty because the workload runtime, not scan_kb, owns conclusions.
type scanKBConclusion struct {
	ID          string  `json:"id"`
	Subject     string  `json:"subject"`
	Predicate   string  `json:"predicate"`
	Object      string  `json:"object"`
	EvidenceIDs []int64 `json:"evidence_ids"`
}

// scanKBLogic is kept for response-shape compatibility. Later Relation Layer
// work may populate relation-path metadata, but scan_kb does not synthesize
// task-level logic.
type scanKBLogic struct {
	ConclusionID string  `json:"conclusion_id"`
	EvidenceIDs  []int64 `json:"evidence_ids"`
}

// scanKBDocumentRef is complete document provenance for related evidence.
type scanKBDocumentRef struct {
	KnowledgeBaseID int64  `json:"kb_id"`
	DocumentID      int64  `json:"document_id"`
	RevisionID      int64  `json:"revision_id"`
	Title           string `json:"title"`
}

// scanKBEvidenceRef is complete, non-truncated evidence provenance. Content
// is intentionally absent so this metadata remains available under any text
// budget and can be used with read_kb_evidence.
type scanKBEvidenceRef struct {
	ChunkID         int64  `json:"chunk_id"`
	KnowledgeBaseID int64  `json:"kb_id"`
	DocumentID      int64  `json:"document_id"`
	RevisionID      int64  `json:"revision_id"`
	DocumentTitle   string `json:"document_title"`
	LocatorJSON     string `json:"locator_json"`
	ExpansionKind   int    `json:"expansion_kind"`
}

// scanKBEvidenceBody is the only scan response field whose content is subject
// to the token budget. Its metadata always matches related_evidence exactly.
type scanKBEvidenceBody struct {
	ChunkID int64  `json:"chunk_id"`
	Content string `json:"content"`
}

// scanKBResponse is a structured evidence package. The final two fields are
// deliberately ordered last: a truncation notice must be the final information
// supplied to an agent when evidence bodies exceed the text budget.
type scanKBResponse struct {
	Status               int                  `json:"status"`
	Outcome              string               `json:"outcome"`
	SupportedConclusions []scanKBConclusion   `json:"supported_conclusions"`
	Logic                []scanKBLogic        `json:"logic"`
	RelationPaths        []kb.RelationPath    `json:"relation_paths"`
	RelatedDocuments     []scanKBDocumentRef  `json:"related_documents"`
	RelatedEvidence      []scanKBEvidenceRef  `json:"related_evidence"`
	Evidence             []scanKBEvidenceBody `json:"evidence"`
	EvidenceTruncated    bool                 `json:"evidence_truncated"`
	TruncationNotice     string               `json:"truncation_notice,omitempty"`
}

// Execute runs the semantic search with authorization enforcement.
func (s *ScanKBTool) Execute(args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		applogger.Warn("scan_kb: empty query rejected", "args", args)
		return "", fmt.Errorf("query is required")
	}
	reason, _ := args["reason"].(string)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		applogger.Warn("scan_kb: missing reason; continuing with empty query-intent trace", "query_fingerprint", kb.QueryFingerprint(query))
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
		resp, _ := json.Marshal(emptyScanKBResponse(kb.ScanStatusInsufficientEvidence))
		return string(resp), nil
	}
	if kbID != 0 {
		if _, ok := authorized[kbID]; !ok {
			applogger.Warn("scan_kb: unauthorized KB requested", "kb_id", kbID, "authorized", authorizedKBNames(kbs))
			return "", fmt.Errorf("knowledge base %d is not authorized to you; authorized KBs: %s", kbID, authorizedKBNames(kbs))
		}
	}

	ctx := context.Background()
	var result *kb.ScanResult
	ids := make([]int64, 0, len(kbs))
	for _, item := range kbs {
		ids = append(ids, item.ID)
	}
	if kbID != 0 {
		ids = []int64{kbID}
	}
	applogger.Debug("scan_kb: retrieval started", "kb_ids", ids, "query_fingerprint", kb.QueryFingerprint(query), "reason_fingerprint", kb.QueryFingerprint(reason), "document", docTitle, "top_k", topK)
	result, err = kb.ScanMultiKBEvidence(ctx, ids, query, kb.ScanOptions{TopK: topK, DocumentTitle: docTitle, ExpandContext: true, ExpandRelations: true})
	if err != nil {
		applogger.Error("scan_kb: search failed", "kb_id", kbID, "document", docTitle, "error", err)
		return "", fmt.Errorf("knowledge base search failed: %w", err)
	}
	if result == nil {
		applogger.Error("scan_kb: retrieval returned no result object", "kb_ids", ids, "query_fingerprint", kb.QueryFingerprint(query), "document", docTitle)
		return "", fmt.Errorf("knowledge base search returned no result")
	}
	applogger.Debug("scan_kb: retrieval completed", "kb_ids", ids, "query_fingerprint", kb.QueryFingerprint(query), "reason_fingerprint", kb.QueryFingerprint(reason), "document", docTitle, "evidence_count", len(result.Evidence), "related_evidence_count", len(result.RelatedEvidence), "relation_path_count", len(result.RelationPaths), "top_k", topK)
	resp := buildScanKBResponse(result, scanKBEvidenceTokenBudget)
	s.recordUsageTrace(query, reason, ids, kbID, docTitle, resp)
	out, _ := json.Marshal(resp)
	return string(out), nil
}

// emptyScanKBResponse makes even no-access and no-evidence responses conform
// to the structured contract, avoiding fragile result-shape branching in agents.
func emptyScanKBResponse(status int) scanKBResponse {
	outcome := "no active evidence found"
	return scanKBResponse{
		Status:               status,
		Outcome:              outcome,
		SupportedConclusions: []scanKBConclusion{},
		Logic:                []scanKBLogic{},
		RelationPaths:        []kb.RelationPath{},
		RelatedDocuments:     []scanKBDocumentRef{},
		RelatedEvidence:      []scanKBEvidenceRef{},
		Evidence:             []scanKBEvidenceBody{},
	}
}

// buildScanKBResponse exposes deterministic evidence provenance and truncated
// evidence bodies. Task-level conclusions and logic remain empty in Phase A.
func buildScanKBResponse(result *kb.ScanResult, evidenceTokenBudget int) scanKBResponse {
	response := emptyScanKBResponse(result.Status)
	response.Outcome = result.Outcome
	response.SupportedConclusions = []scanKBConclusion{}
	response.Logic = []scanKBLogic{}
	response.RelationPaths = append([]kb.RelationPath(nil), result.RelationPaths...)

	response.RelatedDocuments = documentRefs(result.RelatedEvidence)
	response.RelatedEvidence = evidenceRefs(result.RelatedEvidence)
	// Bodies are emitted from the full structurally related evidence set. The
	// planner-admission subset is not an output cap: all discovered metadata and
	// body entries remain addressable even when body content is truncated.
	trimmedEvidence, truncated, usedTokens := kb.TruncateEvidenceContent(result.RelatedEvidence, evidenceTokenBudget)
	response.Evidence = make([]scanKBEvidenceBody, 0, len(trimmedEvidence))
	for _, item := range trimmedEvidence {
		response.Evidence = append(response.Evidence, scanKBEvidenceBody{ChunkID: item.RetrievalUnitID, Content: item.Content})
	}
	response.EvidenceTruncated = truncated
	if truncated {
		response.TruncationNotice = fmt.Sprintf("Evidence content was truncated at the %d estimated-token budget (%d emitted, estimated from character length). Related document and chunk metadata is complete; use read_kb_evidence with chunk_ids to read full evidence bodies.", evidenceTokenBudget, usedTokens)
	}
	return response
}

// recordUsageTrace persists one KB usage call for the enclosing workload trace.
// Retrieval remains usable if trace persistence fails; relation analysis is
// enqueued when the workload exits so cross-scan relations can be discovered.
func (s *ScanKBTool) recordUsageTrace(query, reason string, authorizedKBIDs []int64, requestedKBID int64, documentFilter string, response scanKBResponse) {
	authorizedJSON, err := json.Marshal(authorizedKBIDs)
	if err != nil {
		applogger.Error("scan_kb: failed to marshal authorized KB scope for trace", "error", err)
		authorizedJSON = []byte("[]")
	}
	handlesJSON, err := json.Marshal(response.RelatedEvidence)
	if err != nil {
		applogger.Error("scan_kb: failed to marshal evidence handles for trace", "error", err)
		handlesJSON = []byte("[]")
	}
	truncated := 0
	if response.EvidenceTruncated {
		truncated = 1
	}
	trace := model.KBUsageTrace{
		WorkID:                s.traceContext.WorkID,
		SessionID:             s.traceContext.SessionID,
		Query:                 query,
		Reason:                reason,
		AuthorizedKBIDsJSON:   string(authorizedJSON),
		RequestedKBID:         requestedKBID,
		DocumentFilter:        documentFilter,
		EvidenceHandlesJSON:   string(handlesJSON),
		ResultStatus:          response.Status,
		ReturnedEvidenceCount: len(response.RelatedEvidence),
		ReturnedDocumentCount: len(response.RelatedDocuments),
		EvidenceBodyTruncated: truncated,
		QueryFingerprint:      kb.QueryFingerprint(query),
		ReasonFingerprint:     kb.QueryFingerprint(reason),
	}
	if err := dops.CreateKBUsageTrace(&trace); err != nil {
		applogger.Error("scan_kb: failed to persist KB usage trace", "work_id", s.traceContext.WorkID, "session_id", s.traceContext.SessionID, "query_fingerprint", kb.QueryFingerprint(query), "error", err)
		return
	}
	applogger.Debug("scan_kb: KB usage trace persisted", "trace_id", trace.ID, "work_id", s.traceContext.WorkID, "session_id", s.traceContext.SessionID, "query_fingerprint", kb.QueryFingerprint(query))
}

// documentRefs returns complete, deterministic document provenance without
// applying a size cap. Metadata must remain available even when body text is not.
func documentRefs(evidence []kb.Evidence) []scanKBDocumentRef {
	refsByID := make(map[int64]scanKBDocumentRef, len(evidence))
	for _, item := range evidence {
		refsByID[item.DocumentID] = scanKBDocumentRef{KnowledgeBaseID: item.KnowledgeBaseID, DocumentID: item.DocumentID, RevisionID: item.RevisionID, Title: item.DocumentTitle}
	}
	refs := make([]scanKBDocumentRef, 0, len(refsByID))
	for _, ref := range refsByID {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].DocumentID < refs[j].DocumentID })
	return refs
}

// evidenceRefs returns complete, deterministic chunk provenance without body text.
func evidenceRefs(evidence []kb.Evidence) []scanKBEvidenceRef {
	refs := make([]scanKBEvidenceRef, 0, len(evidence))
	for _, item := range evidence {
		refs = append(refs, scanKBEvidenceRef{ChunkID: item.RetrievalUnitID, KnowledgeBaseID: item.KnowledgeBaseID, DocumentID: item.DocumentID, RevisionID: item.RevisionID, DocumentTitle: item.DocumentTitle, LocatorJSON: item.LocatorJSON, ExpansionKind: int(item.ExpansionKind)})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ChunkID < refs[j].ChunkID })
	return refs
}

// ReadKBEvidenceTool progressively discloses full evidence bodies by immutable
// chunk ID. It has no access to retrieval plans or filesystem paths.
type ReadKBEvidenceTool struct {
	authorize     KBAuthorizer
	CycleDetector // Embedded: cycle detection on (args, result) pairs
}

// NewReadKBEvidenceTool creates a direct evidence reader bound to call-time
// authorization checks.
func NewReadKBEvidenceTool(authorize KBAuthorizer) *ReadKBEvidenceTool {
	return &ReadKBEvidenceTool{authorize: authorize}
}

// Execute reads active-revision evidence while enforcing current KB grants.
func (t *ReadKBEvidenceTool) Execute(args map[string]interface{}) (string, error) {
	chunkIDs := readChunkIDs(args["chunk_ids"])
	if len(chunkIDs) == 0 {
		applogger.Warn("read_kb_evidence: missing chunk_ids", "args", args)
		return "", fmt.Errorf("chunk_ids is required")
	}
	tokenBudget := scanKBEvidenceTokenBudget
	if value, ok := args["max_tokens"].(float64); ok && int(value) > 0 {
		tokenBudget = int(value)
	}
	if tokenBudget > readKBEvidenceMaxTokens {
		applogger.Warn("read_kb_evidence: max_tokens capped", "requested_max_tokens", tokenBudget, "max_tokens", readKBEvidenceMaxTokens)
		tokenBudget = readKBEvidenceMaxTokens
	}

	_, authorized, err := authorizedKBSet(t.authorize)
	if err != nil {
		return "", fmt.Errorf("failed to load authorized knowledge bases: %w", err)
	}
	evidence, unavailableIDs, err := loadAuthorizedEvidence(chunkIDs, authorized)
	if err != nil {
		applogger.Error("read_kb_evidence: failed to load evidence", "chunk_ids", chunkIDs, "error", err)
		return "", fmt.Errorf("failed to load evidence: %w", err)
	}
	if len(unavailableIDs) > 0 {
		applogger.Warn("read_kb_evidence: requested evidence is unavailable", "chunk_ids", unavailableIDs)
	}

	trimmed, truncated, usedTokens := kb.TruncateEvidenceContent(evidence, tokenBudget)
	response := readKBEvidenceResponse{
		RequestedChunkIDs:   append([]int64(nil), chunkIDs...),
		UnavailableChunkIDs: make([]int64, len(unavailableIDs)),
		Evidence:            make([]readKBEvidenceBody, 0, len(trimmed)),
		EvidenceTruncated:   truncated,
	}
	copy(response.UnavailableChunkIDs, unavailableIDs)
	for _, item := range trimmed {
		response.Evidence = append(response.Evidence, readKBEvidenceBody{
			ChunkID:         item.RetrievalUnitID,
			KnowledgeBaseID: item.KnowledgeBaseID,
			DocumentID:      item.DocumentID,
			RevisionID:      item.RevisionID,
			DocumentTitle:   item.DocumentTitle,
			LocatorJSON:     item.LocatorJSON,
			Content:         item.Content,
		})
	}
	if truncated {
		response.TruncationNotice = fmt.Sprintf("Evidence content was truncated at the %d estimated-token budget (%d emitted, estimated from character length). All requested evidence metadata is retained; request the remaining chunk IDs again to continue.", tokenBudget, usedTokens)
	} else if len(unavailableIDs) > 0 {
		response.TruncationNotice = "Some requested evidence is unavailable because it is unauthorized, deleted, or no longer the active document revision. All requested and unavailable IDs are retained above."
	}
	applogger.Debug("read_kb_evidence: completed", "chunk_count", len(chunkIDs), "estimated_token_budget", tokenBudget, "estimated_used_tokens", usedTokens, "truncated", truncated)
	out, _ := json.Marshal(response)
	return string(out), nil
}

// readKBEvidenceBody keeps each requested chunk's full active-revision
// provenance adjacent to its content. Only Content may be truncated.
type readKBEvidenceBody struct {
	ChunkID         int64  `json:"chunk_id"`
	KnowledgeBaseID int64  `json:"kb_id"`
	DocumentID      int64  `json:"document_id"`
	RevisionID      int64  `json:"revision_id"`
	DocumentTitle   string `json:"document_title"`
	LocatorJSON     string `json:"locator_json"`
	Content         string `json:"content"`
}

// readKBEvidenceResponse keeps the required truncation notice as its final
// field so agents always see the boundary after the evidence list.
type readKBEvidenceResponse struct {
	RequestedChunkIDs   []int64              `json:"requested_chunk_ids"`
	UnavailableChunkIDs []int64              `json:"unavailable_chunk_ids"`
	Evidence            []readKBEvidenceBody `json:"evidence"`
	EvidenceTruncated   bool                 `json:"evidence_truncated"`
	TruncationNotice    string               `json:"truncation_notice,omitempty"`
}

// readChunkIDs parses a JSON function-call array without accepting duplicate,
// zero, or negative IDs. Invalid entries are logged by the caller as missing
// evidence instead of being silently converted into a database query.
func readChunkIDs(value interface{}) []int64 {
	values, ok := value.([]interface{})
	if !ok {
		return nil
	}
	ids := make([]int64, 0, len(values))
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		idValue, ok := value.(float64)
		id := int64(idValue)
		if !ok || id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// loadAuthorizedEvidence reads only chunks that belong to an authorized KB and
// the document's current active revision. It returns unavailable IDs rather
// than exposing stale or unauthorized content.
func loadAuthorizedEvidence(chunkIDs []int64, authorized map[int64]struct{}) ([]kb.Evidence, []int64, error) {
	var chunks []model.DocumentChunk
	if err := database.DB.Where("id IN ? AND deleted = 0", chunkIDs).Find(&chunks).Error; err != nil {
		return nil, nil, err
	}
	chunkByID := make(map[int64]model.DocumentChunk, len(chunks))
	for _, chunk := range chunks {
		chunkByID[chunk.ID] = chunk
	}
	documentIDs := make([]int64, 0, len(chunks))
	seenDocumentIDs := make(map[int64]struct{}, len(chunks))
	for _, chunk := range chunks {
		if _, exists := seenDocumentIDs[chunk.DocumentID]; exists {
			continue
		}
		seenDocumentIDs[chunk.DocumentID] = struct{}{}
		documentIDs = append(documentIDs, chunk.DocumentID)
	}
	var documents []model.Document
	if len(documentIDs) > 0 {
		if err := database.DB.Where("id IN ?", documentIDs).Find(&documents).Error; err != nil {
			return nil, nil, err
		}
	}
	documentByID := make(map[int64]model.Document, len(documents))
	for _, document := range documents {
		documentByID[document.ID] = document
	}
	locators, err := evidenceLocators(chunkIDs)
	if err != nil {
		return nil, nil, err
	}

	evidence := make([]kb.Evidence, 0, len(chunkIDs))
	unavailable := make([]int64, 0)
	for _, chunkID := range chunkIDs {
		chunk, exists := chunkByID[chunkID]
		if !exists {
			unavailable = append(unavailable, chunkID)
			continue
		}
		document, exists := documentByID[chunk.DocumentID]
		if !exists || document.Status != model.DocumentStatusReady || document.ActiveRevisionID != chunk.RevisionID {
			unavailable = append(unavailable, chunkID)
			continue
		}
		if _, exists := authorized[document.KnowledgeBaseID]; !exists {
			unavailable = append(unavailable, chunkID)
			continue
		}
		content := chunk.DisplayText
		if content == "" {
			content = chunk.Content
		}
		locator := locators[chunkID]
		if locator == "" {
			locator = "{}"
		}
		evidence = append(evidence, kb.Evidence{KnowledgeBaseID: document.KnowledgeBaseID, DocumentID: document.ID, DocumentTitle: document.Title, RevisionID: chunk.RevisionID, RetrievalUnitID: chunk.ID, Content: content, LocatorJSON: locator, ExpansionKind: 0})
	}
	return evidence, unavailable, nil
}

// evidenceLocators resolves at most one canonical node locator per chunk. A
// chunk may map to multiple nodes; the lowest mapping ordinal is stable and is
// enough to navigate back to the containing document region.
func evidenceLocators(chunkIDs []int64) (map[int64]string, error) {
	var mappings []model.DocumentChunkNode
	if err := database.DB.Where("chunk_id IN ?", chunkIDs).Order("chunk_id ASC, ordinal ASC").Find(&mappings).Error; err != nil {
		return nil, err
	}
	nodeIDs := make([]int64, 0, len(mappings))
	for _, mapping := range mappings {
		nodeIDs = append(nodeIDs, mapping.NodeID)
	}
	var nodes []model.ContentNode
	if len(nodeIDs) > 0 {
		if err := database.DB.Where("id IN ?", nodeIDs).Find(&nodes).Error; err != nil {
			return nil, err
		}
	}
	nodeByID := make(map[int64]model.ContentNode, len(nodes))
	for _, node := range nodes {
		nodeByID[node.ID] = node
	}
	locators := make(map[int64]string, len(mappings))
	for _, mapping := range mappings {
		if _, exists := locators[mapping.ChunkID]; exists {
			continue
		}
		if node, exists := nodeByID[mapping.NodeID]; exists {
			locators[mapping.ChunkID] = node.LocatorJSON
		}
	}
	return locators, nil
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
	DocumentID       int64     `json:"document_id"`
	ActiveRevisionID int64     `json:"active_revision_id"`
	Title            string    `json:"title"`
	FileType         string    `json:"file_type"`
	FileSize         int64     `json:"file_size"`
	ChunkCount       int       `json:"chunk_count"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
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
			DocumentID:       d.ID,
			ActiveRevisionID: d.ActiveRevisionID,
			Title:            d.Title,
			FileType:         d.FileType,
			FileSize:         d.FileSize,
			ChunkCount:       d.ChunkCount,
			Status:           documentStatusName(d.Status),
			CreatedAt:        d.CreatedAt,
		})
	}

	out, _ := json.Marshal(listKBDocumentsResponse{KBID: kbID, KBName: kbName, Documents: entries})
	return string(out), nil
}
