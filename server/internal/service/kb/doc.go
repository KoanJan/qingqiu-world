// Package kb provides knowledge base management, document processing,
// evidence-grounded retrieval, and workload-driven semantic expansion.
//
// This is a package-level singleton service. Call Init(embDim, flatThreshold) once at
// startup, then RecoverProcessingDocuments() to handle interrupted pipelines. Public
// functions (SearchKB, SearchMultiKB, SubmitDocument, etc.) can be called directly.
//
// # Workload-driven Semantic RAG
//
// The KB package implements the RAG side of a Workload-driven Semantic RAG
// architecture:
//
//	The workload runtime owns task reasoning.
//	KB owns evidence retrieval, provenance, and semantic expansion.
//
// In the current product, FocusedWork is the workload runtime that calls
// scan_kb/read_kb_evidence/list_kb_documents. The RAG design itself is not tied
// to the FocusedWork name: any runtime that records query intent, authorized KB
// scope, and returned evidence handles can feed relation maintenance.
//
// The package deliberately does not implement a task-level planner, frontier
// loop, or final-answer generator. scan_kb is one retrieval step plus optional
// structural/relation expansion; if a task needs another investigative step,
// the workload runtime calls scan_kb again.
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
// # Canonical evidence model
//
// Retrieval operates on DocumentChunk, but durable provenance is anchored in an
// immutable document-revision model:
//
//	KnowledgeBase
//	  └── Document
//	        └── DocumentRevision     immutable parse/index version
//	              └── ContentNode    canonical structural node
//	                    └── DocumentChunk / DocumentChunkNode mapping
//
// Document.ActiveRevisionID is the visibility boundary. Search results,
// context expansion, direct evidence reads, relation grounding, and relation
// expansion must resolve back to the active revision. Old revisions can remain
// in storage, but their evidence must not participate in retrieval once a new
// active revision replaces them.
//
// ContentNode is the canonical evidence anchor. DocumentChunk is the retrieval
// unit optimized for vector/BM25 lookup. RelationEvidence stores both: the
// content-node identity for provenance and the chunk identity for efficient
// retrieval expansion.
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
//  3. Create revision: writes DocumentRevision and canonical rendition metadata.
//  4. Store chunks: writes DocumentChunk records to the main DB.
//  5. Store content nodes: writes ContentNode and DocumentChunkNode mappings.
//  6. Embed: batch-embeds chunks (batch size 10) via the configured embedding service.
//  7. Store vectors: inserts vectors into the KB's vectors.db via vectorStore.
//
// Steps 1–7 are executed by documentProcessor.Process (document_processor.go).
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
// # Evidence scan tools
//
// scan_kb, read_kb_evidence and list_kb_documents are exposed through higher
// level tool wrappers. The core implementation lives in internal/service/tools
// and calls back into this package for retrieval.
//
// scan_kb returns a structured evidence package rather than a plain top-k list:
//   - top_k limits base retrieval anchors only.
//   - Context Expansion may add nearby structural evidence from ContentNode
//     siblings.
//   - Relation Expansion may add evidence through grounded/active KBRelation
//     rows.
//   - Evidence metadata is never truncated; only evidence body text can be
//     budget-truncated.
//   - Task-level conclusions stay empty because the workload runtime owns
//     conclusions.
//
// read_kb_evidence is progressive disclosure by chunk ID. It re-checks current
// KB authorization and active-revision visibility at execution time. It is not
// a raw file reader.
//
// # Workload usage traces
//
// scan_kb records KBUsageTrace for work-scoped calls. A trace is the bridge
// between online retrieval and offline relation maintenance:
//
//	scan_kb arguments/result
//	  └── KBUsageTrace
//	        └── relationAnalysisInput + analyzerEvidence
//	              └── relationCandidate
//	                    └── GroundedRelationInput
//	                          └── KBEntity + KBRelation + KBRelationEvidence
//
// A trace stores the query, reason, authorized KB scope, optional requested
// KB/document filter, and the evidence handles returned to the workload
// runtime. The reason is query-intent evidence: it explains why this search was
// made, but it is not KB evidence and cannot support relation truth.
//
// Business fields in KBUsageTrace:
//   - WorkID links all scan_kb calls made inside one workload. It is the
//     relation-analysis aggregation key. Cross-scan relations are discovered
//     only because all traces with the same WorkID are analyzed together.
//   - SessionID records the conversation/session context for audit and
//     troubleshooting. It does not affect grounding or authorization.
//   - Query is the actual retrieval text used for vector/BM25 search.
//   - Reason is the caller's stated intent for the search. It is useful prompt
//     context for the analyzer, but it is not evidence and cannot ground a
//     relation by itself.
//   - AuthorizedKBIDsJSON is the exact KB scope visible to the caller at search
//     time. Relation entities and relations are scoped by this set, not by a
//     single global namespace.
//   - RequestedKBID is the explicit kb_id argument when the caller narrowed the
//     search to one KB. Zero means the authorized KB set was searched.
//   - DocumentFilter is the optional document-title filter requested by the
//     caller. It explains why returned evidence may come from a narrow document
//     subset.
//   - EvidenceHandlesJSON is a JSON array of scanKBEvidenceRef. It intentionally
//     stores complete metadata but no body text, so it remains stable even when
//     response bodies are token-truncated.
//   - ResultStatus records complete/partial/insufficient-evidence status from
//     deterministic retrieval, not LLM confidence.
//   - ReturnedEvidenceCount and ReturnedDocumentCount describe the scan result
//     shape used by background maintenance and debugging.
//   - EvidenceBodyTruncated records whether only the emitted response body was
//     truncated. The metadata in EvidenceHandlesJSON must remain complete.
//   - QueryFingerprint and ReasonFingerprint are log/debug identifiers for the
//     two raw texts. They are not used for evidence grounding.
//
// Work finalization enqueues one relation analysis job for the whole work_id
// when traces exist. scan_kb itself never enqueues relation jobs. This preserves
// cross-scan context: one workload can call scan_kb multiple times, and useful
// relations may only be visible after considering the whole workload trace.
//
// # Candidate to relation admission
//
// Relation maintenance turns workload traces into semantic shortcuts in four
// stages:
//
//  1. Analyzer evidence. EvidenceHandlesJSON is parsed into traceEvidenceHandle
//     values. Each handle is reloaded from DB and validated against an active
//     document, active revision, non-deleted chunk, and ContentNode mapping.
//     Stale or unmapped handles are logged and skipped.
//  2. LLM candidate. The analyzer asks the system LLM for sparse
//     relationCandidate rows containing subject, predicate, object, cited chunk
//     IDs, and an exact support quote. Candidates are untrusted.
//  3. Programmatic grounding. groundedRelationInputFromCandidate accepts only
//     candidates whose cited chunks are visible in the analyzer evidence and
//     whose support quote appears exactly in at least one cited chunk.
//  4. Durable admission. UpsertGroundedRelation validates every evidence handle
//     again, creates/reuses scope-bound KBEntity rows, creates/reuses one
//     idempotent KBRelation row, and stores KBRelationEvidence rows.
//
// The field flow is intentionally strict:
//
//	scanKBEvidenceRef
//	  ChunkID + KB/Document/Revision IDs + LocatorJSON
//	  └── traceEvidenceHandle
//	        same metadata, no body text
//	        └── analyzerEvidence
//	              + Content + primary ContentNodeID + ContentHash
//	              └── relationCandidate
//	                    Subject + Predicate + Object + EvidenceChunks + SupportQuote
//	                    └── RelationGroundingInput
//	                          KB/Document/Revision/ContentNode/Chunk IDs
//	                          + LocatorJSON + SupportQuote + ContentHash
//	                          └── KBRelationEvidence
//	                                durable proof row for KBRelation
//
// Fields in the online scan response:
//   - scanKBDocumentRef.KnowledgeBaseID/DocumentID/RevisionID/Title identifies
//     every document represented in the evidence package. It is complete
//     metadata and must not be truncated.
//   - scanKBEvidenceRef.ChunkID is the public evidence handle consumed by
//     read_kb_evidence and persisted into KBUsageTrace.EvidenceHandlesJSON.
//   - scanKBEvidenceRef.KnowledgeBaseID, DocumentID, RevisionID and
//     DocumentTitle keep the handle self-describing. The analyzer rechecks
//     these IDs against DB state; it does not trust the JSON blindly.
//   - scanKBEvidenceRef.LocatorJSON describes where the evidence came from
//     inside the canonical document rendition. It is copied into relation
//     evidence when the handle becomes a proof anchor.
//   - scanKBEvidenceRef.ExpansionKind records why the evidence was returned:
//     direct anchor, structural context, or relation expansion. It explains
//     provenance, not truth or score.
//   - scanKBEvidenceBody.ChunkID joins body text back to scanKBEvidenceRef.
//   - scanKBEvidenceBody.Content is the only scan result field that may be
//     token-truncated. IDs, titles, locators, and expansion kinds must remain
//     complete so the workload can request full bodies later.
//
// Fields used only inside the analyzer:
//   - traceEvidenceHandle mirrors scanKBEvidenceRef. It exists in this package
//     to avoid importing the tool package back into kb.
//   - analyzerEvidence.Handle keeps the original evidence handle.
//   - analyzerEvidence.Content is reloaded from the active chunk and is the
//     only body text shown to the LLM relation analyzer.
//   - analyzerEvidence.ContentNodeID is the primary canonical node mapped from
//     the chunk. It becomes the durable provenance anchor if a candidate is
//     admitted.
//   - analyzerEvidence.ContentHash is copied from the ContentNode. Later
//     validation can mark the relation stale if the canonical evidence changes.
//   - relationAnalysisInput.WorkID identifies the workload being analyzed.
//   - relationAnalysisInput.Guidance and FinalOutput provide compact workload
//     context. They may help the LLM decide which relation candidates are
//     useful, but they cannot ground relation truth.
//   - relationAnalysisCall.TraceID links prompt context back to KBUsageTrace.
//   - relationAnalysisCall.Query and Reason preserve what was searched and why.
//     They are prompt context only; support must come from evidence snippets.
//
// Fields in relationCandidate:
//   - Subject and Object are LLM-proposed display labels. They are not entity
//     IDs and are not trusted as canonical names.
//   - Predicate is an LLM-proposed open-vocabulary edge label. It is normalized
//     before persistence.
//   - EvidenceChunks lists chunk IDs from the provided analyzer evidence only.
//     A candidate is rejected if it cites an unseen chunk.
//   - SupportQuote must be an exact substring of at least one cited chunk. This
//     is the main boundary between LLM suggestion and programmatic grounding.
//
// Fields in GroundedRelationInput and RelationGroundingInput:
//   - GroundedRelationInput.ScopeKBIDs is the KB authorization scope for the
//     relation namespace. If omitted, admission derives it from evidence KB IDs.
//   - SubjectLabel/ObjectLabel/Predicate are the candidate labels before
//     normalization. They are used to create/reuse entity and relation rows.
//   - ApplicabilityNote preserves natural-language proposition boundaries such
//     as time range, version, project scope, jurisdiction, or source assumption.
//     It is not parsed into a condition schema and is not treated as proof.
//   - Evidence is the list of grounded proof handles. At least one handle is
//     required.
//   - PolicyVersion is part of relation idempotency. Changing the admission
//     policy can create a separate relation row without corrupting old rows.
//   - RelationGroundingInput.KnowledgeBaseID, DocumentID, RevisionID,
//     ContentNodeID and ChunkID point to active KB evidence. ValidateRelationGrounding
//     requires KB/document/revision/content-node IDs and verifies the optional
//     chunk when supplied.
//   - RelationGroundingInput.LocatorJSON is copied from the content node or
//     evidence handle so users and debugging tools can locate the support.
//   - RelationGroundingInput.SupportQuote is persisted as the human-readable
//     proof fragment and must appear in the canonical evidence carrier.
//   - RelationGroundingInput.ContentHash snapshots the canonical content node
//     state. A mismatch means the evidence changed and the relation should no
//     longer be trusted.
//
// Durable relation fields:
//   - KBEntity.ScopeHash and ScopeKBIDsJSON bind entity identity to the KB set
//     visible when the relation was derived. The same label in a different KB
//     scope is a different lightweight entity.
//   - KBEntity.NormalizedLabel is the deduplication key inside the scope.
//   - KBEntity.DisplayLabel preserves a readable label from the candidate.
//   - KBEntity.State controls whether the entity can participate in traversal.
//   - KBRelation.ScopeHash and ScopeKBIDsJSON repeat the scope on the edge so
//     relation expansion can enforce authorization without loading both
//     entities first.
//   - KBRelation.SubjectEntityID, Predicate and ObjectEntityID form the semantic
//     edge. Predicate is normalized open vocabulary, not a fixed enum.
//   - KBRelation.ApplicabilityNote carries natural-language limits for the
//     relation proposition. Repeated admissions for the same scoped
//     subject-predicate-object relation preserve distinct notes instead of
//     overwriting them, because these limits are intentionally not structured.
//   - KBRelation.State controls traversal. Only grounded/active relation states
//     are intended for semantic expansion; stale/rejected/archived rows are
//     audit or maintenance records.
//   - KBRelation.IdempotencyKey prevents duplicate subject-predicate-object
//     rows in the same scope and policy version.
//   - KBRelation.EvidenceHash summarizes the sorted proof fingerprints. It
//     changes when the supporting evidence set changes.
//   - KBRelation.PolicyVersion records which admission rules produced the row.
//   - KBRelation.UseCount and LastUsedAtUnix are objective maintenance
//     telemetry, not proof.
//   - KBRelation.StaleReason explains why a relation was removed from retrieval
//     paths.
//   - KBRelationEvidence.RelationID attaches one proof row to its relation.
//   - KBRelationEvidence.KnowledgeBaseID/DocumentID/RevisionID/ContentNodeID/ChunkID
//     preserve the exact active evidence path used for grounding.
//   - KBRelationEvidence.LocatorJSON, SupportQuote and ContentHash preserve
//     human-readable location, quote-level support, and invalidation state.
//   - KBRelationEvidence.EvidenceFingerprint deduplicates proof rows and is
//     independent of relation IDs before persistence.
//
// LLM output is therefore never treated as truth. The LLM may propose; the
// program decides whether the proposal is grounded and whether it can become
// retrieval metadata.
//
// # Relation expansion
//
// Relation expansion starts from retrieved chunk IDs, finds grounded/active
// relations supported by those chunks, checks that the relation scope is within
// the caller's authorized KB set, and maps relation evidence rows back into
// active chunk evidence. Relation paths are provenance metadata, not conclusions.
// The workload runtime remains responsible for deciding whether another scan
// or a final answer is appropriate.
//
// # Startup recovery (RecoverProcessingDocuments)
//
//   - Documents with status=pending are validated and re-enqueued without
//     changing their visible state.
//   - Documents with status=processing are marked failed (interrupted pipeline).
//   - Ready documents without an active revision are activated only after their
//     revision, chunks, and persisted vectors have been re-verified; otherwise
//     they are marked failed instead of becoming partially visible.
//   - Knowledge bases with index_type=switching are reset to flat (interrupted HNSW build).
//
// # Deletion (DeleteKnowledgeBase)
//
// When a KB is deleted:
//   - The indexManager is released from memory.
//   - KB-owned DB rows are removed in one application-level cascade transaction:
//     access grants, documents, revisions, content nodes, chunks, chunk-node
//     mappings, usage traces, relation jobs, relation evidence, relations, and
//     orphan relation entities.
//   - The KB directory (files + vectors.db + index.bin) is removed after the DB
//     cascade succeeds.
//
// # Shutdown
//
// Shutdown() closes all worker channels, letting goroutines exit when channels
// close, then closes all index managers (releasing vectors.db connections and HNSW graphs).
package kb
