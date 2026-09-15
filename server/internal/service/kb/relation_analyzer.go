package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"qingqiu-world-server/internal/database"
	applogger "qingqiu-world-server/internal/logger"
	"qingqiu-world-server/internal/model"
	"qingqiu-world-server/internal/service/llm"
)

const (
	relationAnalyzerEvidenceLimit            = 12
	relationAnalyzerEvidenceSnippetMaxTokens = 500
)

// traceEvidenceHandle mirrors scan_kb's related_evidence JSON contract without
// importing the tool package back into kb.
type traceEvidenceHandle struct {
	ChunkID         int64  `json:"chunk_id"`
	KnowledgeBaseID int64  `json:"kb_id"`
	DocumentID      int64  `json:"document_id"`
	RevisionID      int64  `json:"revision_id"`
	DocumentTitle   string `json:"document_title"`
	LocatorJSON     string `json:"locator_json"`
	ExpansionKind   int    `json:"expansion_kind"`
}

// relationCandidateOutput is the strict JSON envelope requested from the LLM.
type relationCandidateOutput struct {
	Relations []relationCandidate `json:"relations" jsonschema:"description=Evidence-grounded candidate relations extracted from the supplied evidence only,required"`
}

// relationCandidate is an untrusted LLM proposal. It becomes durable only
// after exact quote grounding against active KB evidence.
type relationCandidate struct {
	Subject        string  `json:"subject" jsonschema:"description=Left entity label exactly supported by the evidence,required"`
	Predicate      string  `json:"predicate" jsonschema:"description=Short open-vocabulary predicate, snake_case preferred,required"`
	Object         string  `json:"object" jsonschema:"description=Right entity label exactly supported by the evidence,required"`
	EvidenceChunks []int64 `json:"evidence_chunk_ids" jsonschema:"description=Chunk IDs from the provided evidence that support this relation,required"`
	SupportQuote   string  `json:"support_quote" jsonschema:"description=Short exact quote copied from one cited evidence chunk,required"`
}

// analyzerEvidence is the canonical active evidence payload visible to the
// relation analyzer for workload-scoped relation discovery.
type analyzerEvidence struct {
	Handle        traceEvidenceHandle
	Content       string
	ContentNodeID int64
	ContentHash   string
}

// relationAnalysisCall is one KB usage call within a workload trace.
type relationAnalysisCall struct {
	TraceID int64
	Query   string
	Reason  string
}

// relationAnalysisInput is the bounded KB-oriented trace passed to the
// Relation Analyzer. It mirrors the Workload-driven Semantic RAG trace without
// exposing full workload internals or non-KB tool output.
type relationAnalysisInput struct {
	WorkID      int64
	Guidance    string
	FinalOutput string
	Calls       []relationAnalysisCall
}

// AnalyzeKBUsageFocus extracts and grounds relation candidates from all KB
// calls made by one workload. The current workload producer is FocusedWork,
// hence the historical function name. Cross-scan relations can only be
// discovered when the analyzer sees the workload-scoped KB trace.
func AnalyzeKBUsageFocus(ctx context.Context, workID int64, llmConfig *model.LLMConfig) error {
	if workID <= 0 {
		return fmt.Errorf("work_id is required")
	}
	applogger.Debug("relation analyzer: workload analysis started", "work_id", workID)
	var traces []model.KBUsageTrace
	if err := database.DB.Where("work_id = ?", workID).Order("id ASC").Find(&traces).Error; err != nil {
		return fmt.Errorf("load KB usage traces for work %d: %w", workID, err)
	}
	applogger.Debug("relation analyzer: workload traces loaded", "work_id", workID, "trace_count", len(traces))
	if len(traces) == 0 {
		applogger.Info("relation analyzer: workload has no KB usage traces, skipping", "work_id", workID)
		return nil
	}

	input := buildFocusRelationAnalysisInput(workID, traces)
	applogger.Debug("relation analyzer: workload input assembled", "work_id", workID, "call_count", len(input.Calls), "guidance_chars", len([]rune(input.Guidance)), "final_output_chars", len([]rune(input.FinalOutput)))
	evidence, err := loadAnalyzerEvidenceFromTraces(traces)
	if err != nil {
		return err
	}
	applogger.Debug("relation analyzer: workload evidence loaded", "work_id", workID, "evidence_count", len(evidence), "evidence_limit", relationAnalyzerEvidenceLimit)
	if len(evidence) == 0 {
		applogger.Info("relation analyzer: workload has no usable evidence, skipping", "work_id", workID, "trace_count", len(traces))
		return nil
	}

	candidates, err := proposeRelationCandidates(ctx, llmConfig, input, evidence)
	if err != nil {
		return err
	}
	applogger.Debug("relation analyzer: workload candidates proposed", "work_id", workID, "candidate_count", len(candidates))
	if len(candidates) == 0 {
		applogger.Debug("relation analyzer: no workload relation candidates", "work_id", workID, "trace_count", len(traces))
		return nil
	}

	evidenceByChunkID := make(map[int64]analyzerEvidence, len(evidence))
	for _, item := range evidence {
		evidenceByChunkID[item.Handle.ChunkID] = item
	}

	admitted := 0
	rejected := 0
	for _, candidate := range candidates {
		input, ok := groundedRelationInputFromCandidate(candidate, evidenceByChunkID)
		if !ok {
			rejected++
			continue
		}
		if _, err := UpsertGroundedRelation(input); err != nil {
			applogger.Warn("relation analyzer: workload candidate rejected by grounding", "work_id", workID, "subject", candidate.Subject, "predicate", candidate.Predicate, "object", candidate.Object, "error", err)
			rejected++
			continue
		}
		admitted++
	}
	applogger.Info("relation analyzer: workload processed", "work_id", workID, "trace_count", len(traces), "evidence_count", len(evidence), "candidate_count", len(candidates), "admitted", admitted, "rejected", rejected)
	return nil
}

// buildFocusRelationAnalysisInput loads workload context that is safe for
// relation discovery: initial guidance and compact final handoff output.
func buildFocusRelationAnalysisInput(workID int64, traces []model.KBUsageTrace) relationAnalysisInput {
	input := relationAnalysisInput{WorkID: workID, Calls: make([]relationAnalysisCall, 0, len(traces))}
	var work model.Work
	if err := database.DB.Select("description").First(&work, workID).Error; err != nil {
		applogger.Warn("relation analyzer: failed to load focus guidance", "work_id", workID, "error", err)
	} else {
		input.Guidance = work.Description
	}
	var handoff model.FocusHandoff
	if err := database.DB.Select("summary").Where("work_id = ?", workID).Order("id DESC").First(&handoff).Error; err == nil {
		input.FinalOutput = handoff.Summary
	}
	for _, trace := range traces {
		input.Calls = append(input.Calls, relationAnalysisCall{TraceID: trace.ID, Query: trace.Query, Reason: trace.Reason})
	}
	return input
}

// loadAnalyzerEvidenceFromTraces resolves all returned evidence handles from a
// workload and deduplicates them by chunk ID before applying the analyzer budget.
func loadAnalyzerEvidenceFromTraces(traces []model.KBUsageTrace) ([]analyzerEvidence, error) {
	dedup := make(map[int64]traceEvidenceHandle)
	orderedHandles := make([]traceEvidenceHandle, 0)
	for _, trace := range traces {
		var handles []traceEvidenceHandle
		if err := json.Unmarshal([]byte(trace.EvidenceHandlesJSON), &handles); err != nil {
			return nil, fmt.Errorf("parse trace evidence handles for trace %d: %w", trace.ID, err)
		}
		for _, handle := range handles {
			if handle.ChunkID <= 0 {
				continue
			}
			if _, exists := dedup[handle.ChunkID]; exists {
				continue
			}
			dedup[handle.ChunkID] = handle
			orderedHandles = append(orderedHandles, handle)
			if len(orderedHandles) == relationAnalyzerEvidenceLimit {
				return loadAnalyzerEvidenceHandles(orderedHandles), nil
			}
		}
	}
	return loadAnalyzerEvidenceHandles(orderedHandles), nil
}

// loadAnalyzerEvidenceHandles resolves evidence handles back to active chunk
// evidence. Stale, deleted or unmapped handles are logged and skipped.
func loadAnalyzerEvidenceHandles(handles []traceEvidenceHandle) []analyzerEvidence {
	result := make([]analyzerEvidence, 0, len(handles))
	for _, handle := range handles {
		var chunk model.DocumentChunk
		if err := database.DB.Where("id = ? AND knowledge_base_id = ? AND document_id = ? AND revision_id = ? AND deleted = 0",
			handle.ChunkID, handle.KnowledgeBaseID, handle.DocumentID, handle.RevisionID).
			First(&chunk).Error; err != nil {
			applogger.Warn("relation analyzer: evidence chunk unavailable", "chunk_id", handle.ChunkID, "error", err)
			continue
		}
		var document model.Document
		if err := database.DB.Select("id, status, active_revision_id").Where("id = ?", handle.DocumentID).First(&document).Error; err != nil {
			applogger.Warn("relation analyzer: evidence document unavailable", "document_id", handle.DocumentID, "error", err)
			continue
		}
		if document.Status != model.DocumentStatusReady || document.ActiveRevisionID != handle.RevisionID {
			applogger.Warn("relation analyzer: evidence revision is no longer active", "chunk_id", handle.ChunkID, "document_id", handle.DocumentID, "trace_revision_id", handle.RevisionID, "active_revision_id", document.ActiveRevisionID, "status", document.Status)
			continue
		}
		nodeID, nodeHash, err := primaryContentNodeForChunk(handle.ChunkID)
		if err != nil {
			applogger.Warn("relation analyzer: evidence has no content-node grounding", "chunk_id", handle.ChunkID, "error", err)
			continue
		}
		content := chunk.DisplayText
		if content == "" {
			content = chunk.Content
		}
		result = append(result, analyzerEvidence{
			Handle:        handle,
			Content:       content,
			ContentNodeID: nodeID,
			ContentHash:   nodeHash,
		})
	}
	return result
}

// primaryContentNodeForChunk returns the stable content-node grounding used to
// validate a relation candidate's support quote.
func primaryContentNodeForChunk(chunkID int64) (int64, string, error) {
	var mapping model.DocumentChunkNode
	if err := database.DB.Where("chunk_id = ?", chunkID).Order("ordinal ASC, node_id ASC").First(&mapping).Error; err != nil {
		return 0, "", err
	}
	var node model.ContentNode
	if err := database.DB.Select("id, content_hash").Where("id = ?", mapping.NodeID).First(&node).Error; err != nil {
		return 0, "", err
	}
	return node.ID, node.ContentHash, nil
}

// proposeRelationCandidates asks the system LLM for sparse relation candidates
// only. The returned candidates are not trusted until programmatic grounding.
func proposeRelationCandidates(ctx context.Context, llmConfig *model.LLMConfig, input relationAnalysisInput, evidence []analyzerEvidence) ([]relationCandidate, error) {
	chatModel := llm.NewChatModelWithTemperature(llmConfig.BaseURL, llmConfig.APIKey, llmConfig.ModelID, llm.TemperatureDeterministic)
	prompt := buildRelationAnalyzerPrompt(input, evidence)
	applogger.Debug("relation analyzer: candidate prompt built",
		"work_id", input.WorkID,
		"call_count", len(input.Calls),
		"evidence_count", len(evidence),
		"prompt_bytes", len(prompt),
		"prompt_fingerprint", QueryFingerprint(prompt),
		"prompt", prompt,
	)
	response, err := chatModel.ChatWithJSONSchema(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, llm.JSONSchemaDefinition{
		Name:        "KBRelationCandidateOutput",
		Description: "Candidate evidence-grounded relations from one workload KB trace",
		Strict:      true,
		Schema:      llm.GenerateSchema[relationCandidateOutput](),
	})
	if err != nil {
		return nil, fmt.Errorf("relation candidate LLM call failed: %w", err)
	}
	var output relationCandidateOutput
	if err := json.Unmarshal([]byte(response), &output); err != nil {
		return nil, fmt.Errorf("parse relation candidate output: %w", err)
	}
	return output.Relations, nil
}

// buildRelationAnalyzerPrompt formats the minimal trace context needed for
// candidate extraction. Evidence snippets are bounded; metadata remains exact.
func buildRelationAnalyzerPrompt(input relationAnalysisInput, evidence []analyzerEvidence) string {
	var b strings.Builder
	b.WriteString("Extract sparse, evidence-grounded relation candidates from one workload-scoped knowledge-base trace.\n")
	b.WriteString("Use only the evidence snippets below. Do not use world knowledge. Do not infer relation truth from the query reason alone.\n")
	b.WriteString("Return only relations whose subject, predicate, object, cited chunk IDs, and support_quote are directly supported by the provided snippets.\n\n")
	if input.Guidance != "" {
		b.WriteString("Workload guidance:\n")
		b.WriteString(truncateTextByTokens(input.Guidance, relationAnalyzerEvidenceSnippetMaxTokens))
		b.WriteString("\n\n")
	}
	b.WriteString("KB calls:\n")
	for index, call := range input.Calls {
		b.WriteString(fmt.Sprintf("\nCall %d trace_id=%d\nQuery: %s\nReason: %s\n",
			index+1,
			call.TraceID,
			call.Query,
			call.Reason,
		))
	}
	if input.FinalOutput != "" {
		b.WriteString("\nFinal workload output excerpt:\n")
		b.WriteString(truncateTextByTokens(input.FinalOutput, relationAnalyzerEvidenceSnippetMaxTokens))
		b.WriteString("\n")
	}
	b.WriteString("\n\nEvidence snippets:\n")
	for _, item := range evidence {
		b.WriteString(fmt.Sprintf("\n[chunk_id=%d document_id=%d revision_id=%d title=%q]\n",
			item.Handle.ChunkID,
			item.Handle.DocumentID,
			item.Handle.RevisionID,
			item.Handle.DocumentTitle,
		))
		b.WriteString(truncateTextByTokens(item.Content, relationAnalyzerEvidenceSnippetMaxTokens))
		b.WriteString("\n")
	}
	return b.String()
}

// groundedRelationInputFromCandidate admits only candidates whose cited chunk
// is visible in this trace and whose exact support quote appears in that chunk.
func groundedRelationInputFromCandidate(candidate relationCandidate, evidenceByChunkID map[int64]analyzerEvidence) (GroundedRelationInput, bool) {
	if NormalizeEntityLabel(candidate.Subject) == "" || NormalizeRelationPredicate(candidate.Predicate) == "" || NormalizeEntityLabel(candidate.Object) == "" {
		return GroundedRelationInput{}, false
	}
	quote := strings.TrimSpace(candidate.SupportQuote)
	if quote == "" {
		return GroundedRelationInput{}, false
	}
	grounding := make([]RelationGroundingInput, 0, len(candidate.EvidenceChunks))
	seen := make(map[int64]struct{}, len(candidate.EvidenceChunks))
	for _, chunkID := range candidate.EvidenceChunks {
		if _, exists := seen[chunkID]; exists {
			continue
		}
		seen[chunkID] = struct{}{}
		item, ok := evidenceByChunkID[chunkID]
		if !ok {
			return GroundedRelationInput{}, false
		}
		if !strings.Contains(item.Content, quote) {
			continue
		}
		grounding = append(grounding, RelationGroundingInput{
			KnowledgeBaseID: item.Handle.KnowledgeBaseID,
			DocumentID:      item.Handle.DocumentID,
			RevisionID:      item.Handle.RevisionID,
			ContentNodeID:   item.ContentNodeID,
			ChunkID:         item.Handle.ChunkID,
			LocatorJSON:     item.Handle.LocatorJSON,
			SupportQuote:    quote,
			ContentHash:     item.ContentHash,
		})
	}
	if len(grounding) == 0 {
		return GroundedRelationInput{}, false
	}
	return GroundedRelationInput{
		SubjectLabel:  candidate.Subject,
		Predicate:     candidate.Predicate,
		ObjectLabel:   candidate.Object,
		Evidence:      grounding,
		PolicyVersion: DefaultRelationPolicyVersion,
	}, true
}
