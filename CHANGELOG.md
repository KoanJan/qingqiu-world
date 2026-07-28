# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.1] - 2026-07-29

### Added
- **Agent Energy System**: each AI agent has a daily energy budget of 100 points (carry-over up to 200 cap), consumed on Decide output; zero energy triggers a hard block — the agent cannot perceive, reason, or respond until the next day. Energy recovers lazily on each Decide trigger with multi-day catch-up, and startup recovery restores energy for offline periods. Global timezone locked on first launch to `<DATA_ROOT>/tz.txt`, independent of OS timezone changes
- **DB Data Migration**: independent `migration` package with semver-based incremental migration — databases at version >= 0.1.0 execute sequential migration scripts per version while older databases are cleared and re-initialized; each version's migration lives in its own file

### Changed
- **Agent List in Chat Sidebar**: the sidebar toggles between session list and agent list views; each agent card shows avatar, name, and current energy with a start-chat button; the standalone new-chat button removed

### Fixed
- **Session Sidebar Tooltip**: hover text changed from "New Chat" to "Sessions" to match its actual function

## [0.1.0] - 2026-07-27

### Added
- **Appearance Personalization**: customizable background (solid color or image) with quick-select color swatches including pure white, custom color picker, and system-preset/user-uploaded image galleries; glass overlay with independent opacity and blur sliders — both sliders now show guidance labels ("Fully Transparent" / "Opaque", "Clear" / "Blurry") at each end; settings persist across restarts

### Changed
- **Default Background**: new installations start with a clean white solid background instead of light gray
- **Splash Screen Redesign**: startup screen changed from dark theme to clean white minimal style with the new app icon
- **Card Border Visibility**: agent cards, LLM configuration cards, and color swatches now have subtle gray borders, ensuring they remain visible against white backgrounds
- **Avatar Upload Limit**: increased from 1 MB to 20 MB

### Fixed
- **Agent Avatar Update**: updating an agent's avatar no longer fails with a database error
- **Agent Empty State**: "No Agent" placeholder text now properly centered when the agent list is empty

## [0.0.35] - 2026-07-21

### Added
- **Skill Frontmatter Parsing**: `ExtractSkillFrontmatter` parses YAML frontmatter (name, description) from SKILL.md files, enabling direct transmission instead of LLM rewriting; `FormatRawWithLineNumbers` formats full content with line numbers for prompt context

### Changed
- **Experience Ingestion Decoupling**: title and description now sourced directly from SKILL.md frontmatter; body content preserved via `LineRange`/`SectionRef` with PRESERVE/SUPPLEMENT/GENERATE three modes — LLM references original content by line numbers instead of rewriting; LLM-generated sections used only as fallback when original sections don't exist
- **Markdown Rendering**: `react-markdown` + `remark-gfm` replaced with `pd-markdown` across `PublicExperienceDetail` and `ChatWindow`, providing built-in full Markdown styling, GFM support, and YAML frontmatter parsing
- **Public Experience Source Display**: SKILL.md source tab now renders frontmatter as a key-value table and body content as Markdown, replacing the old raw text display

## [0.0.34] - 2026-07-18

### Added
- **Engineering Documentation**: two architecture documents covering the agent runtime event loop and the context engineering pipeline, plus package-level `doc.go` for sandbox, experience, memory, and knowledge base
- **Docker Deployment**: single-container deployment with Alpine + nginx + Go backend; `docker compose up -d` in the `docker/` directory starts the full application on port 18888

### Fixed
- **Chat Scroll Animation**: session and tab switching now jumps instantly to the latest message instead of smoothly scrolling from the top; activity list opens at the newest records rather than the oldest
- **Empty State Alignment**: "No activity records" and "No delivered files yet" prompts now consistently centered both horizontally and vertically
- **Stale Chat Content**: creating a new chat without sending a message no longer preserves the previous session's view state (activity or received panel)
- **Modal Auto-Reopen**: creating an LLM config or Agent no longer causes the creation modal to appear automatically on the next settings visit
- **Memory Semantic Propagation**: `HitEmbedding` was never populated in `propagateRetrievalHit`, causing `propagateSemantic` to be unreachable dead code — all three propagation channels (temporal, semantic, same-session) now function correctly


## [0.0.33] - 2026-07-17

### Added
- **Cycle Detection**: each tool now embeds a CycleDetector that tracks consecutive identical `(args, result)` pairs via SHA256 signature; after 3 consecutive repeats a soft warning is appended to the tool result guiding the agent to self-reflect, after 8 the work enters a forced checkpoint (write_notes only) then terminates — detecting behavioral loops without interpreting content

### Changed
- **Decision Route Semantics**: route trigger tightened from "modifies, corrects, or continues an active work" to "carries a new instruction or constraint that changes what the work should do" — events that mention an active work without changing its direction (e.g., progress queries) now fall through to chat, giving users immediate responses
- **Active Works Context**: Decide LLM now sees each active work's running duration and latest notes entry, not just work ID and guidance — enabling better judgment of work state when deciding whether to route new events
- **Notes Structured Storage**: notes migrated from markdown (`notes.md`) to append-only JSONL (`notes.jsonl`), with each entry a self-contained JSON object (timestamp, int type enum, content, references, conflicts_with); storage layer provides stateless read/write APIs keyed by person ID + session ID, and callers render notes independently for their own format needs

### Fixed
- **macOS Sandbox DNS**: Seatbelt `(allow default)` does not cover `mach-lookup` — added explicit allow rules for `mDNSResponder` (DNS resolution) and `configd` (network configuration), fixing all network access failures inside sandboxed processes
- **macOS Sandbox Command Execution**: `darwin.go` was double-wrapping commands in `bash -c`, causing quoting and escaping issues; now passes the command slice directly to `sandbox-exec`, matching the Linux bwrap implementation


## [0.0.32] - 2026-07-16

### Changed
- **Prompt Cache Optimization**: restructured prompt templates across all LLM call sites — static instructions front-loaded, dynamic content moved to end; task loop sliding window replaced with dynamic shrink anchor strategy, guidance history now persisted independently


## [0.0.31] - 2026-07-13

### Changed
- **Chat Context Retrieval**: replaced vector-based semantic search with keyword-based retrieval for session history, eliminating the standalone vector store dependency; query preprocessing now extracts keywords alongside query rewriting
- **Person-Centric Data Model**: internal model renamed from `Agent` to `AgentConfig` to clarify that Person is the first-class entity and Agent is its configuration; 8 business tables now reference Person directly via `person_id` instead of going through AgentConfig

### Removed
- **Dead Code**: `memory.Search` and `VectorStoreService` (single-consumer vector store) cleaned up; `OnRAGHit` renamed to `OnRetrievalHit` to reflect the new retrieval method


## [0.0.30] - 2026-07-12

### Added
- **Task Metadata**: structured metadata (source type, session info) injected into task system prompt, enabling agents to know what triggered their work and query relevant chat histories
- **Search Chat Histories Tool**: new `search_chat_histories` tool for task agents to query chat records by keyword, with results grouped by session
- **Chat Message Copy & Collapse**: message bubbles now include a copy button and auto-collapse for long messages with gradient mask and expand toggle

### Changed
- **Delivery Instructions**: task completion prompt no longer lists file paths; agents direct users to the Received tab since `deliver_to` handles delivery
- **Task Notes On Stop**: notes now written on task stop (not just success), ensuring failure reasons are captured
- **Activity Icons**: bash tool display changed from wrench emoji to `>_` text; tool icons grouped by category
- **Tab Switch Scroll**: switching back to Chat tab now auto-scrolls to latest message
- **Sandbox Policy**: both macOS Seatbelt and Linux bwrap sandboxes changed from deny-default to allow-default; root filesystem mounted read-only with workspace as the only writable area

### Fixed
- **Reflection Notes Error**: reflection now checks interaction records before reading notes.md; sessions without task interactions are skipped with INFO log, while read failures for sessions with interactions are logged as ERROR
- **Sandbox DNS**: macOS sandbox blocked mDNSResponder mach-lookup, causing DNS resolution failure inside sandboxed processes

## [0.0.29] - 2026-07-10

### Added
- **Unified Identity**: agents and users now share a common Person profile with name and bio, replacing the separate agent name/description fields and user profile; agent cards in settings now display the bio

### Changed
- **Delivery Files**: received files are organized by sender name and delivery time (e.g. `Alice_20260710211000_000000`), with collapsible folder tree navigation replacing the flat list and "xx delivery" header
- **View Tabs**: Chat / Activities / Received tab labels now follow the app language setting instead of hardcoded English; app defaults to English on first launch

### Removed
- **Agent Description Field**: the separate description input on the agent configuration form has been removed, superseded by the Person bio

## [0.0.28] - 2026-07-10

### Added
- **File Delivery**: agents can deliver output files to recipients via the `deliver_to` tool, with auto-versioned `delivery_N` subdirectories preventing overwrites across multiple deliveries; a new Received panel lists delivered files with one-click opening via the system default application and "Show in Finder" for each delivery batch
- **View Tab Switcher**: Chat, Activities, and Received views are now three persistent tabs with a sliding pill indicator animation, replacing the previous toggle buttons

### Changed
- **COMPLETION OUTPUT Prompt**: task completion prompt now reminds agents that the recipient cannot see their `output/` directory, and to use `deliver_to` when files are needed; delivery of whole directories is preferred over individual files

## [0.0.27] - 2026-07-09

### Added
- **Sandbox Execution**: kernel-level sandbox for `BashTool` — `sandbox-exec` (Seatbelt) on macOS, `bubblewrap` on Linux, plain exec fallback on Windows; sandbox unavailability falls back without blocking the task

### Changed
- **Architecture-Aware Packages**: macOS and Linux install packages split by CPU architecture (amd64/arm64); filenames include `{arch}` suffix; Linux targets switched from `AppImage + deb` to `deb + rpm`

### Fixed
- **Production Log Visibility**: Go server logs moved from app bundle internals to user data directory; log level defaults to `INFO` in production

## [0.0.26] - 2026-07-07

### Added
- **Text File Tools**: three new agent tools (`read_text_file`, `write_text_file`, `edit_text_file`) for text file operations, avoiding the escaping pitfalls of `echo`, `sed`, and heredoc; supports line-based pagination, atomic overwrite, append mode, and exact substring replacement

### Changed
- **Activity Display**: tool call entries in the activity log now show action and target with distinct visual hierarchy; the activity toggle button is always visible during session execution


## [0.0.25] - 2026-07-06

### Added
- **Tool Output Safety Boundary**: two-layer protection against context window exhaustion from oversized tool outputs — tools self-truncate at the semantic level before serialization to preserve JSON structure integrity, with a system-level byte fallback that discards any output exceeding the hard limit, ensuring tool blowups can never silently overflow the agent's context

### Changed
- **Workspace Directory Layout**: session workspaces reorganized from a flat `{root}/{session_id}` structure to `{root}/{agent_id}/{session_id}`, adding an agent-level directory layer as structural preparation for future multi-agent isolation; all path resolution consolidated into a dedicated workspace package, eliminating scattered path concatenation logic across five packages


## [0.0.24] - 2026-07-05

### Added
- **Activity View**: new aggregation API transforms raw interaction records into a human-readable execution timeline; frontend panel toggles between chat and activity views, with per-row agent identity display supporting future multi-agent scenarios

### Changed
- **Task Completion Semantics**: agent execution prompt changed from user-facing delivery instructions to an internal factual work summary, with presentation responsibility delegated to the Chat LLM

### Removed
- **`has_interactions` Field**: removed across all layers (model, schema, services, runtime, SSE, frontend); activity view availability now determined by the presence of interaction records via API


## [0.0.23] - 2026-07-03

### Changed
- **Experience Toolization**: private experiences changed from fixed system prompt injection to on-demand tools (`scan_my_experience` / `recall_my_experience`)
- **System Prompt Reorganization**: tool description texts and static guidance sections moved out of per-iteration rebuild; OS information added to prompt
- **Experience Reflection**: LLM can now update existing experiences instead of always creating new ones
- **Source Identification**: `AgentExperience.SourceFingerprint` replaced with `SourceID`; fingerprint.txt write timing moved from task completion to reflection end
- **Skill Ingestion UX**: public experiences pre-written on upload with Generating/Active/Error status; redistill API added; UploadedSkill made stateless
- **Learn Judgment**: semantic search threshold raised, explicit rejection heuristics added to judgment prompt, entity self-profiles switched to first-person
- **Experience Extraction Prompt**: distinction clarified between host-environment coupling (strip) and domain technical details (keep)

### Removed
- **Skill Dedup**: fingerprint-based upload deduplication

## [0.0.22] - 2026-07-02

### Added
- **Private Experience System**: heartbeat-triggered reflection automatically distills transferable lessons from task execution notes into structured agent-owned experiences (title, description, when-to-use, guidelines, pitfalls, procedure); experiences are semantically retrieved and injected into task system prompts to guide future execution
- **Public Experience Library**: external Skill files (SKILL.md) are refined into host-agnostic experiences via LLM extraction — method skills preserve actionable advice while reference skills extract structural patterns only; agents learn from the public library through a heartbeat-triggered mechanism that evaluates long-term interaction patterns and mechanically copies relevant experiences into private storage
- **System LLM Configuration**: singleton config table for host-level LLM operations (e.g., skill ingestion) that are not tied to any agent

### Changed
- **Big View Ring Architecture**: settings no longer squeezes the chat view — each is a full-screen view with a sliding track transition; a single animated toggle button cycles through the ring
- **Settings Two-Pane Layout**: replaced the overview card grid with a persistent left navigation sidebar + right content panel, eliminating the overview navigation step
- **Library Sub-view**: merged knowledge base and public experience into a unified Library entry with tab-card switching; both resources share the same navigation slot

### Removed
- **Reset Buttons**: removed meaningless reset buttons from embedding and search config forms


## [0.0.21] - 2026-06-27

### Changed
- **Context Compression Architecture**: replaced scattered `MaybeTriggerSummary` trigger points with a decoupled signal-goroutine system — chat emits `SignalNarrative` only, while dedicated per-session summary and per-agent narrative managers independently decide when to generate via dual thresholds (message count + token estimate); summary generation simplified to zero-recursion range-based function; narrative prompt now injects agent name and character settings for authentic first-person voice
- **Summary Window Default**: raised from 5 to 50 messages to reduce unnecessary compression on short conversations

### Added
- **Cancellable Background Tasks**: summary and narrative goroutines tracked with cancellable contexts, enabling safe abort on session/agent deletion without orphaned database writes
- **Identifier Preservation Protocol**: agent guided via `write_notes` tool description and system prompt to record non-filesystem-recoverable identifiers (API IDs, UUIDs, tokens) in notes, preventing permanent loss across iteration window slides

### Removed
- **Recursive Summary Generation**: eliminated the recursive baseline-chasing logic that created misaligned version chains; baseline now uses the latest existing summary directly


## [0.0.20] - 2026-06-25

### Added
- **Group Chat Foundation**: renamed event types for semantic clarity with private/group distinction; split event type definitions from event bus implementation into separate files; speaker identity carried in message payloads for natural language event context
- **Graceful Shutdown**: two-level WaitGroup tracking ensures agent event loops complete before server exit — active works and draft handlers drain cleanly; SSE connections actively closed before HTTP shutdown; send-to-closed-channel panics caught via recover

### Changed
- **Conversation Summary Model**: split the single summary table into session-level factual summaries and agent-level character-perspective narratives, eliminating redundant LLM calls and preparing the data model for multi-agent group chat
- **LLM Prompt Character Agency**: systematic cleanup removing "user" / "agent" / "the user" from all prompt templates; execution guidance rewritten from second-person task delegation to first-person internal intention; the `[Your Intention]` section is now a pure self-thought without imperative instruction
- **Event Natural Language**: message events described as natural conversation ("{name} talks to you: ...") instead of mechanical system labels, enabling persona-based cognitive framing
- **Comprehension Pipeline**: preprocessing and person state inference run concurrently, reducing per-event latency
- **Logging**: DEBUG level auto-enables source file location; package-level log functions use variable-style declarations
- **Shutdown Scripts**: SIGTERM grace period extended from fixed 2s to polling loop with configurable timeout

### Fixed
- **Agent Status Freeze**: status update used the global DB handle while a transaction held the single connection, causing the query to block until 30-second timeout — status change moved to after transaction commit

## [0.0.19] - 2026-06-24

### Changed
- **Runtime File Split**: split the monolithic `agent_runtime.go` into separate files by responsibility — heartbeat, draft commits, SSE hooks, and alarm management
- **Alarm System Refactoring**: moved alarm goroutine lifecycle from tools layer to runtime layer; tools now only create DB records and emit events, while runtime handles goroutine registration, startup recovery of orphan alarms, and shutdown cleanup

### Removed
- **Proactive Message Mechanism**: removed heartbeat-driven `selfReflect` — event-driven notification via eventqueue replaces periodic LLM polling for proactive replies

### Fixed
- **Transactional Integrity**: wrapped `newWork` and `commitDraft` database operations in transactions to prevent orphan records on partial failure; eliminated nullable `DraftID` on Work model
- **Electron Graceful Shutdown**: aligned Electron's force-kill timeout (5s → 12s) with backend HTTP server's graceful shutdown window, preventing premature SIGKILL during cleanup


## [0.0.18] - 2026-06-23

### Added
- **Cognitive Order Refactoring**: restructured the agent pipeline from "Decide → Execute (mixed)" to "Comprehend → Decide → Execute" three-stage architecture, ensuring decisions are always based on complete comprehension results
- **Compound Decision Model**: Decide phase can now produce multiple actions (create/route/cancel) in a single decision, enabling compound decisions like "cancel work A and create work B"
- **Guidance Channel**: running tasks can receive new directives during execution for mid-course corrections
- **Delivery Guidance**: prompt-level soft guidance with zero-friction delivery principle — agents determine deliverable form and provide absolute paths or direct content

### Changed
- **Guidance Replaces Rewrite**: the Comprehend-Decide pipeline now produces guidance that directly becomes the task requirement, eliminating the old rewrite step
- **Interaction Records by Work**: interaction records now grouped by work instead of draft, with a new record type for route/cancel directives

### Removed
- **task.md Mechanism**: removed system-managed task.md — guidance in the execution directive covers this functionality
- **DeliveryType Preset**: removed hardcoded delivery type selection — replaced by flexible delivery guidance
- **Interaction API**: removed interaction query endpoints and frontend modal — interaction records are now an internal execution trace

### Fixed
- **Enum Column Convention**: migrated all enum columns from text to integer across 4 tables, enforcing the project convention that all enums must use int


## [0.0.17] - 2026-06-18

### Added
- **Structured Output Compatibility Layer**: automatic two-level fallback (json_schema → function_call) for models that don't support `response_format.type: json_schema` (e.g., DeepSeek). Persistent capability cache (`(base_url, model_id)` keyed) avoids repeated trial-and-error across restarts
- **Global DB Query Timeout**: `DefaultContextTimeout: 30s` in GORM config prevents indefinite blocking on database operations

### Fixed
- **PersonState False Positives**: missing role context in person state inference caused casual greetings ("Are you asleep?") to be misclassified as requiring world interaction. Fixed by injecting `agentName` + `characterSettings` into the prompt with identity-driven design
- **PersonState Consumption in Simple Context**: `assembleSimpleContext` (V < N branch) was not consuming `personStateResult`, causing inferred state to be silently discarded

## [0.0.16] - 2026-06-14

### Added
- **Memory System**: event-driven observation recording with use-dependent importance scoring, daily multiplicative decay, semantic retrieval, and relevance propagation. Includes LLM-driven EntityProfile generation — per-entity (user/agent/session) narrative profiles from top-ranked observations, with MD5 dedup and fresh generation each time
- **Identity-Driven Prompt Architecture**: all prompt templates use the agent's actual name instead of "AI assistant," and message evidence labels use real names instead of "Assistant"/"User" — rooted in the sycophancy literature and episodic memory theory
- **User System**: `User` model with profile endpoints and frontend form; user name propagates through all prompt construction paths
- **Unified API Response**: all handlers return HTTP 200 with business codes in the body, transparently unwrapped by a frontend axios interceptor
- **Embedding Guard**: `RequireEmbedding` middleware blocks API requests when embedding configuration is absent, preventing silent failures

### Changed
- **User State Model**: person-state semantics replace user-centric framing — schema descriptions shifted from "the user" to "the person" for neutrality

### Fixed
- **Silent Error Handling**: ~90 instances of `_ = db.XXX(...)` replaced with proper checks across 17 files, using a six-category strategy

## [0.0.15] - 2026-06-10

### Added
- **Intelligent Decision System**: LLM-based semantic decision with 5 action types (ReplyNow, ReplyThenWork, WorkOnly, Ignore, Defer), replacing hardcoded respond-only behavior. Non-message events use rule-based routing, new messages use LLM + JSON Schema
- **Semantic Work Routing**: LLM compares event content against active work descriptions to determine event ownership, with zero-cost skip when no active works exist
- **Heartbeat Introspection**: LLM-based periodic self-check across all participant sessions, with adaptive intervals — active (5min) → steady (30min) → dormant (2h) — driven by idle tick counter
- **Scheduled Events**: `scheduled_event` model and `wake_me_when` tool enabling agents to set self-reminders and be awakened via `EventTypeScheduled`
- **Aggregated Initialization**: `runtime.Start()` single entry point combining callback setup, runtime manager creation, and eager agent startup

### Changed
- **Context Propagation**: all `context.Context` removed from struct fields; single root context in runtime manager propagates cancellation through the entire runtime→work tree
- **Work Lifecycle**: `newWork` only creates objects without starting goroutines; work stops on context cancellation without reverse-controlling runtime
- **Event Queue**: package-level singleton functions (`Subscribe`/`Unsubscribe`/`Send`) replace exported global variable; initialization decoupled from runtime
- **Minimal Exposure**: kb, handler, database packages — all internal-only types and functions made package-private

### Fixed
- **Goroutine Management**: eliminated standalone `cancel` storage; graceful shutdown via single `cancelAll()` cascading through context tree
- **Work Cancellation**: pipeline errors from context cancellation correctly call `abandon()` instead of `handleChatError()`

### Removed
- Dead code: `types.go` (runtime), `NotifyAgentNewMessage` (eventqueue), `GetContextMessages` and `BuildSystemPrompt` (chatctx)


## [0.0.14] - 2026-06-09

### Added
- **Agent Runtime**: new runtime architecture with AgentRuntime/Work/Manager, implementing ReAct loop with Bash and WebSearch tools, SSE-based status push, and participant status tracking (idle/working)
- **Agent Status Bar**: chat window top bar showing agent avatars with animated status indicators (green=idle, pulsing blue=working), agent name on hover
- **Message Draft Model**: interaction records now associated with drafts instead of messages, enabling proper message isolation between user-agent and agent-world boundaries
- **Participant Session Model**: tracks session participants with type/role/status, supporting future multi-agent scenarios
- **Pre-release Data Reset**: 0.0.x versions automatically wipe user data on version change to avoid schema migration issues

### Changed
- **Enum Storage Convention**: all enum fields across 4 tables (participant_sessions, messages, documents, knowledge_bases) migrated from string to int, with database rebuild migration and string→int value mapping
- **Message Role**: `role` field changed from string ("user"/"assistant") to int (1/2) across backend models, API schemas, and frontend types
- **Interaction Query**: API adjusted to query interactions via draft_id instead of user_msg_id + agent_msg_id

### Fixed
- **Database Migration Detection**: enum migration check now correctly handles VARCHAR columns (not just TEXT), fixing silent migration skip
- **Message List Styling**: CSS class mapping updated after role type change from string to int

## [0.0.13] - 2026-06-07

### Fixed
- **LLM Hallucination Prevention**: discard tool_call reasoning content in TaskLoop to prevent internal process information from leaking into chat layer, which caused the chat LLM to misinterpret reasoning (e.g., "the command is correct") as accomplished facts (e.g., "the service is running")
- **Service Degradation Fix**: relax trigger message check in assembleSimpleContext from "must be the latest completed message" to "must exist in the completed messages list", fixing false degradation in concurrent/multi-agent scenarios
- **SQLite ID Reuse Prevention**: add ensureAutoIncrement to rebuild tables with AUTOINCREMENT keyword, ensuring primary key IDs are strictly monotonically increasing and never reused after row deletion
- **Goroutine Leak on Session Deletion**: add TaskCancelManager to cancel running processChatTask goroutines when their session is deleted, preventing stale goroutines from overwriting data

### Changed
- **Message Rendering**: remove streaming chunk rendering in favor of whole-message updates with loading spinner, eliminating chunk interleaving issues in multi-agent scenarios
- **SSE Connection Management**: establish persistent SSE connection per session instead of per-message, with auto-reconnect on session switch

## [0.0.12] - 2026-06-05

### Changed
- **Session List UI**: flattened two-level Collapse structure to single list sorted by updated_at, agent avatar displayed on left side of each session item
- **New Session Button**: toolbar-style MessageCircle icon with agent dropdown selection

### Fixed
- **HNSW Index**: catch panic when adding node at runtime via safeAddToGraph, consistent with batch build behavior


## [0.0.11] - 2026-05-11

### Added
- **Knowledge Base**: full KB lifecycle management — create, delete, document upload, async processing pipeline (extraction → chunking → embedding → indexing), per-KB isolated SQLite vector storage
- **HNSW Index**: dual-mode indexing (Flat → HNSW) with auto-switching by vector count, CAS-based concurrent transition, lazy loading, and startup recovery
- **RAG Integration**: Agent binds knowledge bases; chat pipeline retrieves from bound KBs concurrently and injects results into context
- **Knowledge Base UI**: list/detail views with document upload, status tracking, and KB binding in Agent config

### Changed
- **Chat Pipeline**: reorganized with KB retrieval step between preprocessing and context assembly
- **Document Deletion**: Document records hard-deleted, chunks soft-deleted with `deleted_count` tracking

### Fixed
- **Chat Window**: SSE reconnection on page refresh during streaming, race condition on session switch, temp-to-real session transition preserving streaming state


## [0.0.10] - 2026-05-08

### Added
- ResizableCard component for draggable card-based UI layout

### Changed
- BashTool: use `cmd /c` on Windows instead of `bash -c`
- Embedding option label: "Default" → "Not used"
- Chat input placeholder: "Finally remembered me? Shift+Enter for new line"
- Replace console.log with logger in api.ts

### Fixed
- Agent avatar not persisted when creating with avatar selected


## [0.0.9] - 2026-05-06

### Added
- **Electron Desktop Application**: cross-platform desktop packaging for macOS, Windows, and Linux with one-click build commands (`npm run dist:mac|win|linux`)
- **Go Backend**: complete rewrite of backend from Python to Go with native cross-compilation support (GOOS/GOARCH)
- **Dynamic Port Allocation**: Electron main process automatically assigns free port to Go server, frontend dynamically resolves via IPC
- **Custom Title Bar**: VS Code-style title bar with `titleBarStyle: 'hidden'` + `titleBarOverlay`, native window controls (traffic lights on macOS, min/max/close on Win/Linux) overlaid on web content
- **Splash Screen**: loading screen during Go server startup with health check polling
- **IPC Bridge**: secure communication between renderer and main process via contextBridge (`getServerPort`, `getAppVersion`, `getPlatform`, `onBackendStatus`, `onBackendError`)

### Changed
- **Backend Stack**: FastAPI → Gin, SQLAlchemy → GORM, langchain-openai → go-openai, numpy → custom cosine similarity
- **SQLite Driver**: switched to pure-Go `modernc.org/sqlite` (no CGO required) enabling hassle-free cross-compilation
- **Service Scripts**: `start.sh`/`stop.sh`/`restart.sh` converted from Python (uvicorn) to Go binary management with PID file tracking
- **Vite Config**: `base: './'` for relative paths, `outDir` to project-level `web-dist/` for electron-builder packaging
- **Header Styling**: height reduced from 56px to 38px, logo/icon/text scaled down to match native window controls

### Removed
- **Python Backend**: entire `server/app/` directory (API, services, models, schemas) deleted
- **Python Dependencies**: `pyproject.toml`, `setup.sh`, `venv/` no longer needed
- **Manual Database Init**: `server/database/` removed (Go uses GORM AutoMigrate)
- **Unused Assets**: `web/public/icons.svg` (social icon sprite with no references)

### Performance
- Backend binary size reduced from ~100 MB+ (PyInstaller) to ~33 MB (go build)
- Startup time reduced from 2-5 seconds (Python interpreter) to <1 second (native binary)
- Cross-platform builds now possible from a single macOS machine


## [0.0.8] - 2026-05-01

### Changed
- **Database Migration: MySQL → SQLite**: replaced MySQL with SQLite for desktop application compatibility, eliminating the need for users to install and configure a database server
- **Engine Configuration**: removed MySQL-specific `pool_pre_ping` and `pool_recycle`, added SQLite `check_same_thread=False` and WAL mode pragma
- **SQL Migration Scripts**: consolidated all incremental SQL files (0.0.1 through 0.0.7) into a single `full_init.sql` for fresh database initialization
- **Database Initialization**: `init_db.sh` now supports `init` (full) and `upgrade` (incremental) modes, uses `sqlite3` client
- **ORM Models**: removed `comment=` parameters (SQLite does not support column comments); fixed `SearchConfig.updated_at` to be NOT NULL with server_default
- **Task Loop LLM Configuration**: checkpoint client now uses agent's LLM config instead of separate environment variables, eliminating redundant configuration
- **Data Directory**: unified all application data under `~/PrivateBuddyData/` (db, chroma, workspace, avatars)
- **Environment Variables**: simplified to `DATA_ROOT` only; `DATABASE_URL`, `CHROMA_PERSIST_DIR`, and `LLM_*` variables removed

### Removed
- **pymysql dependency**: no longer needed after SQLite migration
- **LLM environment variables**: `LLM_BASE_URL`, `LLM_MODEL`, `LLM_API_KEY` replaced by database-stored agent LLM configs
- **migrate_mysql_to_sqlite.py**: one-time migration script deleted after use

### Added
- **Auto data directory creation**: `PrivateBuddyData/db` directory automatically created on application startup
- **SQLite PRAGMA configuration**: WAL journal mode and foreign keys enabled on connection
- **Database Version Tracking**: `db_versions` table and `DBVersion` model for schema version management
- **Version API**: `GET /api/version` endpoint returns database schema version from `db_versions` table
- **Upgrade SQL Directory**: `sql/upgrade/` for incremental schema changes in future versions


## [0.0.7] - 2026-05-01

### Added
- **Cached Narrative Generation**: narrative generated alongside summary in background task, stored in `historical_summaries.narrative` field with atomic write, eliminating real-time narrative generation bottleneck during chat processing
- **CachedStaticFiles**: custom StaticFiles class with `Cache-Control: public, max-age=86400` for avatar images, enabling browser-side caching

### Changed
- **Parallel LLM Calls**: User State inference and Query Preprocessing now run concurrently via `asyncio.gather` when V >= N, reducing combined latency from sum to max of both calls
- **Narrative Retrieval**: follows same versioning policy as summary — get latest available version without requiring alignment with current message count
- **Segments Section**: RAG-retrieved segments now rendered as independent section with narrative-style transition in context assembly

### Performance
- Chat response time reduced from 60-90s to 25-50s (V >= N scenarios)
- Avatar HTTP requests eliminated for 24h after first load via browser caching


## [0.0.6] - 2026-05-01

### Added
- **Markdown Rendering**: assistant messages rendered with react-markdown + remark-gfm
- **Custom Agent Avatar**: upload, store, and display custom avatar images for each agent
- **Project Logo**: display favicon.svg in header next to app title
- **Message Time Formatting**: contextual display — same day (time only), yesterday ("Yesterday" + time), older (full date + time) with i18n support
- **Agent Avatar in Chat**: AI messages show the agent's avatar alongside the name
- **LoadingSpinner Component**: braille rotation animation replacing typing dots for streaming messages
- **ConfigIcon Component**: unified icon rendering for agent/LLM/embedding/search/language types
- **CSS Theme Variables**: centralized `--color-*` variables for consistent theming

### Changed
- **Settings Panel**: restructured as right-side drawer with card grid overview instead of main area switching
- **Language Switching**: from dropdown menu to card-based selection
- **Agent List**: removed expand/collapse arrows, added avatar display
- **Settings Labels**: simplified ("LLM Config" → "LLM", "Agent Config" → "Agent", etc.)
- **Inline Colors**: replaced hardcoded hex values with CSS variables across all components

### Fixed
- Scroll-to-top issue when opening historical sessions


## [0.0.5] - 2026-04-30

### Added
- **Finite Working Memory**: iteration window for context visibility, older iterations discarded directly
- **Reader-Oriented Notes**: structured append-only notes.md via write_notes tool, bridging LLM statelessness
- **Forced Checkpoint**: mandatory notes write when distance from last write reaches window boundary
- **Workspace Structure**: `.meta/` (task.md + notes.md) + `output/` two-channel isolation
- **Task Requirement Rewriting**: ambiguous user messages rewritten into clear, self-contained task descriptions

### Changed
- Refactored `agent/` to `task/` for naming consistency with task execution semantics
- Moved chat context logic under `chat/` and created shared DTO module to eliminate circular imports


## [0.0.4] - 2026-04-26

### Added
- **Agent Execution System**: ReAct pattern with minimal tool set (Bash + Web Search)
- **Interaction Boundary**: separate storage for agent-world interactions, isolated from user conversation
- **Search Engine Integration**: configurable Tavily/DuckDuckGo with automatic tool availability detection


## [0.0.3] - 2026-04-24

### Added
- **Narrative Optimization**: internal focalization for background story, cohesive section transitions
- **User State Inference**: three-dimensional model (emotion, purpose, situation) for response strategy guidance


## [0.0.2] - 2026-04-22

### Added
- Context engineering: automatic conversation summary and background narrative generation
- Smart query preprocessing: query classification, rewriting, and clarification
- Character settings: customizable agent personality and style
- RAG integration: retrieve relevant historical context for better responses

### Changed
- Improved context assembly with decoupled summary and recent messages
- Optimized LLM prompts for better multilingual support


## [0.0.1] - 2026-04-17

### Added
- Basic chat functionality with AI agents
- Agent and LLM configuration management
- Session and message history
- SSE streaming for chat responses
- Multi-language support (English and Chinese)
