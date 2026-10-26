// Package experience manages agent experience creation, retrieval, and learning.
//
// This is a package-level singleton service. Call Init(embeddingSvc) once at startup,
// then use package-level functions directly.
//
// # Two sides of experience
//
// Private Experience (agent's own):
//   - Created via Reflection: the agent notes down structured lessons from
//     Workspace notes (CheckReflection, reflection.go). Uses SHA-256 fingerprinting
//     on notes.jsonl to detect changes and avoids redundant re-reflection.
//   - Created via Learning: the agent discovers public experiences worth adopting
//     (CheckLearning, learning.go). Uses semantic search against the agent's
//     session-level entity profiles to find relevant public experiences, then asks
//     the LLM to judge which ones are worth mechanically copying.
//   - All private experiences are stored as AgentExperience + AgentExperienceVector,
//     with Source/SourceID tracking the origin (1=historical session notes,
//     2=Learn/public_experience_id, 4=Workspace notes).
//
// Public Experience (shared across agents):
//   - Created via Ingestion: raw SKILL.md content is distilled by the system LLM
//     (IngestSkill / processIngestion, ingestion.go). A pre-write pattern is used:
//     PublicExperience is created with Status=Generating (empty content), then a
//     background goroutine distills and finalizes. This lets the frontend show the
//     record immediately.
//   - Re-distillable: RedistillPublicExperience() re-runs distillation for an
//     ingestion-sourced experience stuck in Error state.
//   - All public experiences are stored as PublicExperience + PublicExperienceVector.
//
// # Retrieval
//
// Both sides use the same progressive disclosure pattern (two-stage retrieval):
//  1. scan_my_experience tool: semantic search against experience descriptions
//     (SearchExperiences), returning only title + description for the agent to pick.
//  2. recall_my_experience tool: the agent fetches the full record by ID.
//
// This avoids polluting the working context with full experience bodies until
// the agent explicitly chooses one.
//
// Public experience search (SearchPublicExperiences) is used during Learning
// (against entity profile narratives as queries).
//
// # Design decisions
//
//   - The ingestOutput struct mirrors reflectOutput: public and private experiences
//     share the same structured schema (title, description, when_to_use, guidelines,
//     pitfalls, procedure) so that they are interleavable in prompts.
//   - A runtime sync.Map (processingLocks) prevents concurrent ingestion goroutines
//     for the same uploaded skill. This is runtime-only (lost on restart), which is
//     acceptable since a restart means no goroutines are running.
//   - The RedistillPublicExperience flow resets content fields to empty before
//     re-running, treating the existing record as a new pre-write.
package experience
