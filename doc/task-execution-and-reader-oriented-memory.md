# Finite Memory & Reader-Oriented Notes: How LLM Statelessness Shapes Task Execution

How we design around the architectural reality of LLM statelessness — each API call is an independent instance with no shared hidden state — through finite working memory, reader-oriented shared context, and the complementary roles of world state (what) and notes (why).

---

## The Problem

When an autonomous agent executes a multi-step task, it must maintain coherence across many iterations. Each iteration produces observations, decisions, and intermediate results. The agent needs to remember what it has done, why it made certain choices, and what remains — otherwise it loops, contradicts itself, or loses progress.

The naive approach is to keep the entire interaction history in context. But this doesn't scale: as the task grows, token costs increase, latency rises, and the LLM's ability to utilize long contexts degrades (Liu et al., 2023). The standard fallback is system-compressed summaries — but the system is not the executor. It doesn't know which details matter for future reasoning.

A deeper problem underlies both approaches: **LLM calls are stateless.** Each API invocation is independent. The instance that writes a note and the instance that reads it share no hidden state — no preferences, no working assumptions, no implicit context. This is not a limitation to be worked around; it is an architectural reality that our design must account for.

This fact has a precise real-world counterpart: **shift handoff.** When one nurse ends a shift and another begins, the outgoing nurse cannot transfer their memories, intuitions, or tacit understanding of the patient. The only channel for continuity is what is explicitly written in the handoff document and what can be directly observed in the patient's current state. The incoming nurse starts with no shared hidden state — exactly like a new LLM call.

Two questions follow:

1. **How much history should the agent see?** The answer is constrained by the statelessness reality: since instances share no hidden state, the only history that matters is what is explicitly encoded in the prompt. The design question becomes how much explicit context to provide — and what to leave out, forcing the agent to externalize critical information.
2. **Who should decide what gets remembered?** The system (via compression) or the agent (via deliberate writing)? In shift handoff, the outgoing nurse writes the handoff document — not a supervisor who observed the shift from outside. The executor is uniquely qualified to judge what the next executor needs to know.

Our answers: **finite working memory** and **reader-oriented notes**, supported by the complementary roles of **world state** (what happened) and **notes** (why it was done).

---

## Part 1: Theoretical Foundation

### Shift Handoff: The Right Analogy for LLM Statelessness

The intuitive analogy for notes is personal note-taking: "I write things down so I don't forget." This analogy is misleading because it assumes the writer and reader are the same entity with shared implicit context. A person writing notes for their future self can afford to be terse — they assume their future self will remember the surrounding context.

LLM statelessness breaks this assumption. Each API call is a fresh instance. The instance that writes a note and the instance that reads it are more like two nurses at a shift change than one person across time.

**Shift handoff protocols** in high-reliability organizations (aviation, healthcare, nuclear power) address exactly this problem: how to transfer critical information between operators who share no implicit state. Research on these protocols identifies three recurring challenges (Patterson & Wears, 2010; Reason, 1990):

| Challenge | In shift handoff | In LLM task execution |
|-----------|-----------------|----------------------|
| **Information filtering** | The outgoing nurse cannot write everything; they must judge what the incoming nurse needs to know | The agent must decide which information is important enough to persist in notes |
| **Self-containedness** | The handoff document must be understandable without access to the outgoing nurse's thoughts | Notes must be understandable without the writing instance's implicit context |
| **What vs. Why** | The patient's vital signs (what) are directly observable; the reasoning behind treatment choices (why) must be explicitly written | The workspace state (what) is directly observable via bash; the reasoning behind decisions (why) must be explicitly written in notes |

The third challenge reveals a critical distinction: **world state provides what, notes provide why.** These are orthogonal information types. A handoff document that only records what happened (without explaining why) forces the incoming nurse to re-derive the reasoning — wasting time and risking different conclusions. A handoff document that only records why (without grounding in observable state) is unverifiable — the incoming nurse cannot confirm whether the reasoning still applies.

**Design implication**: Our system must provide both channels. The `output/` directory (world state) provides what — directly observable through bash commands. The `notes.md` file provides why — explicitly written by the agent for future readers. Neither channel alone is sufficient; together they form a complete information transfer. Note that what/why is a distinction of primary information type, not an absolute dichotomy: code comments may contain why, and progress entries in notes may contain what. The point is that each channel's *primary* role is distinct, and neither channel's primary role can be served by the other.

### Transactive Memory: Distributed Memory Across Instances

**Transactive Memory Systems** (Wegner, 1987) describe how groups collectively remember through a division of labor: each member knows who knows what, and information retrieval happens through directed communication rather than individual recall. The system's memory capacity exceeds any individual's because knowledge is distributed and accessed through a shared directory of expertise.

In our context, the "group" is the sequence of LLM instances that execute a task. They cannot share implicit state, but they can share an explicit communication channel — notes.md. This is a form of distributed memory: no single instance holds all the information, but the collective (across instances, mediated by notes) maintains a coherent record of the task's progress and reasoning.

A separate but complementary principle from shift handoff research: **the operator who performed the action is the authority on why it was performed** (Patterson & Wears, 2010). System compression cannot replicate this because the system is not the executor — it observes from outside and cannot distinguish critical reasoning from incidental detail. This is why shift handoff protocols in high-reliability organizations require the outgoing operator (not a supervisor) to write the handoff document. The operator has first-hand knowledge of what they did and why. A supervisor watching from outside might produce a more "objective" summary, but would miss the subjective reasoning that is critical for continuity.

**Design implication**: Notes are written by the agent (the executor), not by the system (the observer). This is not just a design preference — it follows from the principle that the executor is the authority on their own reasoning.

### Finite Working Memory: From Scalability to Necessity

Two independent constraints shape our context management design:

1. **Scalability requires a finite window.** Keeping the entire interaction history in context doesn't scale — token costs increase linearly, latency rises, and LLM utilization of long contexts degrades (Liu et al., 2023). A finite window is a practical necessity for any non-trivial task.

2. **LLM statelessness makes the window irreversible.** In a stateful system, information that scrolls out of the window could still be recalled from internal state. But LLM calls are stateless — each invocation is independent, with no cross-call hidden state. Information that scrolls out of the window is not merely hard to access; it is gone entirely, because the LLM has no mechanism to recall it.

These two constraints combine to create a sharp requirement: **if information is important for future iterations, it must be explicitly externalized before it scrolls out of the window.** There is no fallback, no internal recall, no partial recovery. This is the same constraint a shift handoff operates under: once the outgoing nurse leaves, their memories are inaccessible. The handoff document is not written under "pressure" — it is written because there is no other channel.

**What about providing history retrieval as a safety net?** This would create a misalignment with the statelessness reality. If the agent can retrieve old history, the design pretends that statefulness exists — but only partially and unreliably. This is worse than honest statelessness because it creates a false sense of continuity. More concretely: history retrieval provides both what and why, making notes redundant. If the agent can look up why a decision was made in the history, it has no reason to write that reasoning in notes. Notes quality degrades, and the system falls back to implicit history search — which is exactly the "entire history in context" approach that doesn't scale.

**Why output/ is not a safety net**: The workspace's physical state (files, directories, code) provides what — the current state of the world. This is not a "safety net" in the sense of history retrieval because it provides orthogonal information. The agent can observe that a file exists and what it contains, but cannot observe why it was written that way. Output/ and notes serve complementary roles:

| Information type | Source | Can it be lost? |
|-----------------|--------|----------------|
| **What** — current world state | output/ (observable via bash) | No — physical state persists across LLM calls |
| **Why** — reasoning behind decisions | notes.md (explicitly written) | Yes — if not written before scrolling out of the window |

This complementarity is why we allow output/ as an information source but not history retrieval: output/ provides what notes cannot (ground truth about the current world), and notes provide what output/ cannot (reasoning that is invisible in the physical state). History retrieval would provide what notes already provide (why), making notes redundant.

### Recency Effect and Physical State Sedimentation

When notes exceed their size limit, we trim the oldest entries and preserve the newest. Two independent lines of reasoning support this choice:

**Cognitive science — recency effect**: The most recently acquired information exerts the strongest influence on current decisions (Murdoch, 1962). This is reinforced by the **Lost in the Middle** phenomenon (Liu et al., 2023): LLMs utilize information in a U-shaped pattern, with beginning and end of context more effectively leveraged than the middle. Since notes are appended chronologically, the newest entries are at the file's tail — a position of high utilization.

**Shift handoff practice — physical state sedimentation**: In shift handoff, early decisions have had more time to manifest in the observable world. A treatment decision made 8 hours ago has already produced observable effects in the patient's vital signs — the incoming nurse can verify it directly. A decision made 30 minutes ago may not yet have produced observable effects — the reasoning behind it exists only in the handoff document. Similarly, early agent decisions have already been materialized into output/ files (high redundancy with physical state), while recent decisions may not yet be reflected in the workspace (low redundancy with physical state).

| Entry position | Redundancy with output/ | Loss if trimmed |
|---------------|----------------------|----------------|
| Oldest entries | High — results already materialized in files | Low — what is observable, why can often be inferred from what |
| Newest entries | Low — results may not yet be materialized | High — neither what nor why is available elsewhere |

Trimming the oldest entries minimizes information loss because the information they contain has the highest redundancy with the observable world state.

---

## Part 2: The Finite Memory Architecture

### Workspace: Two Complementary Channels

Each task execution operates within an isolated workspace that provides two complementary information channels:

```
~/QingqiuWorldData/workspace/{agent_id}/{session_id}/
    .meta/
        notes.md         # System-managed: reader-oriented shared context (why)
        fingerprint.txt  # System-managed: reflection dedup hash
    output/              # Agent's working directory (what — observable world state)
```

The `.meta/` and `output/` directories serve fundamentally different roles:

| Directory | Information type | Managed by | Agent access | Purpose |
|-----------|-----------------|-----------|-------------|---------|
| `.meta/` | Why (reasoning) | System | Read-only via context system; write via `write_notes` tool | Decision rationale, progress tracking |
| `output/` | What (world state) | Agent | Full read-write via bash | Code, files, deliverables — the materialized results of actions |

This separation is not arbitrary. It mirrors the shift handoff distinction: the patient chart (managed, structured, records reasoning) vs. the patient's physical state (directly observable, records outcomes). The incoming nurse reads the chart for why and observes the patient for what. The incoming LLM instance reads notes.md for why and observes output/ for what.

The `.meta/` isolation is enforced by the bash tool: any command referencing `.meta` is intercepted. The agent cannot directly modify its own handoff document — it must use the structured `write_notes` tool, which enforces append-only semantics. This prevents the agent from accidentally overwriting its own continuity record.

### Fixed Part + Dynamic Window

The agent's context is assembled from two distinct parts:

```
messages = [
    system:  system_prompt + [Context Information]    ← Fixed (always complete)
    user:    [Your Notes]\n{notes.md}                 ← Fixed (always complete)
    ...dynamic messages (last w iterations)            ← Window-controlled
]
```

**Fixed part** — the system prompt (which includes the Guidance as a self-directive) and notes.md are always fully included. These are the agent's essential prerequisites: what it needs to accomplish (Guidance in the system prompt), and why things are the way they are (notes.md). Removing either would be like asking an incoming nurse to continue care without the handoff document or the treatment plan.

**Dynamic part** — only the last `w` iterations (default: 10) are visible. Older iterations are discarded entirely — not compressed, not archived, not retrievable. This is the finite working memory that makes explicit information transfer necessary.

Why iterations rather than tokens? Iterations are discrete, predictable units that align naturally with the checkpoint mechanism. Token budgets require real-time calculation and vary wildly in information density across messages. The iteration window provides a stable, controllable constraint.

### Context Information: Making Statelessness Explicit

At the end of the system prompt, dynamic context information is injected:

```
[Context Information]
Your working memory is limited. You can see the last 10 iterations.
This task has produced 35 iterations total, 25 of which are outside your visible range.

Your NOTES are currently 3200 chars (max: 5000 chars).
```

This serves two purposes:

1. **Explicit statelessness** — the agent is told exactly how much it cannot see. This is honest: rather than pretending the agent has full context, we make the boundary visible. An agent that knows 25 iterations are invisible is better equipped to decide what to write in notes than one that doesn't know what it's missing.
2. **Notes space awareness** — the agent knows how much room remains. An agent approaching the notes limit must prioritize, reinforcing the reader-oriented principle: only information whose loss would harm the task deserves to be written.

---

## Part 3: Reader-Oriented Notes

### Why Notes Are Written for Future Readers

The conventional framing treats notes as self-reminder: "I'm writing this so I don't forget." This framing assumes the writer and reader share context — but with LLMs, they don't. Each API call is a fresh instance, analogous to a new nurse starting a shift.

```
Traditional:  agent acts → system compresses history → agent continues
Our approach: agent acts → agent writes notes for a future LLM reader → next LLM reads notes and continues
```

The shift handoff analogy makes the design choice obvious: the outgoing nurse writes the handoff document for the incoming nurse, not for themselves. The document must be self-contained because the incoming nurse has no access to the outgoing nurse's thoughts. Similarly, notes must be self-contained because the reading LLM instance has no access to the writing instance's implicit context.

This repositioning is reflected throughout the implementation:

- `"Write self-contained entries (future LLM calls have no memory)"` — the writer is told: you are writing for someone who doesn't know what you know
- `"Your notes will help the next execution continue from where you left off."` — at final iteration: notes are for the *next* execution
- `"This will help you continue work if the user requests changes later."` — on success: notes are for *subsequent modifications*
- `"A choice you made (explain why)"` — the decision type requires reasoning, because the reader doesn't know the writer's thought process

The fundamental problem with system compression: **the system is not the executor. It cannot know which information is important for future reasoning.** When the agent writes notes for a future reader, it makes this judgment itself — and as the executor, it is uniquely qualified to do so, just as the outgoing nurse is the authority on their own clinical reasoning.

### Three Guarantees Against LLM Statelessness

LLM statelessness introduces three engineering risks. The current implementation provides three corresponding guarantees:

| Risk | Guarantee | Mechanism |
|------|-----------|-----------|
| Later LLM call overwrites earlier entries | Append-only format | Each `write_notes` call appends a structured entry (timestamp + type + content + references + conflicts_with). No entry is ever overwritten. |
| Later LLM call cannot verify earlier decisions | Verifiable anchors | The `references` field links entries to workspace file paths. When a reader doubts a decision, it can trace the reference to the source file and establish new grounding. |
| LLM writes without awareness of existing entries | Write-after-read by architecture | Notes.md is part of the fixed context — every LLM call sees the complete notes before deciding whether to write. This isn't an extra "review step"; it's an architectural guarantee. |

These guarantees address different levels of the problem. The append-only format prevents destructive interference at the storage level. The references field provides grounding in world state (what) at the semantic level. The fixed-context inclusion ensures the writer has access to existing entries at the architectural level.

**An honest caveat**: architectural guarantee (notes are in the context) is not the same as cognitive guarantee (the LLM fully processes every entry). Long notes may suffer from the Lost in the Middle effect — entries in the middle of notes.md may receive less attention than those at the beginning or end. This is a limitation shared by all long-context LLM applications, not specific to our design. Our mitigation is two-fold: the sliding window trimming strategy keeps notes concise (reducing the length that triggers Lost in the Middle), and the references field allows the reader to verify claims against the observable world state in output/.

### Checkpoint: Mandatory Handoff Documentation

The agent can voluntarily call `write_notes` at any time. But voluntary action alone is insufficient — the agent may be deep in a task and not externalize critical information before it scrolls out of the window.

In high-reliability organizations, shift handoff protocols include **mandatory handoff documentation**: the outgoing nurse is not permitted to leave until they have completed a written handoff document (Patterson & Wears, 2010). This is not because nurses are unreliable — it is because the cost of missing information is high, and voluntary handoff (verbal only) has been shown to omit critical details at unacceptable rates (Reason, 1990). The mandatory documentation requirement ensures that externalization happens regardless of the operator's judgment in the moment.

Our checkpoint mechanism serves the same purpose: it forces the agent to explicitly externalize its current understanding at regular intervals, rather than relying on voluntary action alone.

```
last_notes_iteration = 0  (initial)

Iteration 5:  agent voluntarily calls write_notes → last_notes_iteration = 5
Iterations 6-14: agent does not call write_notes
Iteration 15: distance = 15 - 5 = 10 = window → forced checkpoint triggered
              → only write_notes tool is available
              → agent writes notes → last_notes_iteration = 15
              → all tools restored, normal execution continues
```

During a checkpoint iteration, the agent's tool set is restricted to `write_notes` only. It cannot execute bash commands or search the web — it must externalize its current state before proceeding. This is mandatory handoff documentation: the agent cannot "leave" (continue to the next iteration) until it has written down what the next instance needs to know.

Five note-writing triggers exist in total:

| Trigger | Condition | Purpose |
|---------|-----------|---------|
| Voluntary write | Agent decides to call write_notes | Agent-driven information persistence |
| Forced checkpoint | Distance from last write ≥ window | Mandatory handoff documentation at regular intervals |
| Completion update | Task finishes successfully | Ensure notes reflect final state for future modifications |
| Final iteration save | Max iterations reached | Leave a continuation point for next execution |
| Exception loss | LLM invocation error | Notes are not updated; recent progress may be lost |

The last row is not a mechanism but an acknowledgment. When the LLM call itself fails, notes cannot be updated. The loss boundary is bounded: at most `window` iterations of **unwritten why** (reasoning not yet persisted in notes) and **unmaterialized what** (operations not yet reflected in output/). Everything before the last checkpoint is safe — it is either in notes (why) or in output/ (what). This bounded loss is acceptable because the checkpoint mechanism ensures that the loss window is at most `window` iterations wide.

### Sliding Window Trimming

Notes have a character limit (`notes_max_chars`, default: 5000). When exceeded:

1. **Keep the newest** — trim from the top (oldest entries), preserve the bottom (newest entries)
2. **Align to entry boundaries** — find the next `## [timestamp]` marker after the trim point, to avoid splitting an entry mid-sentence
3. **Mark the trim** — prepend `[notes.md trimmed: older entries discarded]` so the reader knows context is incomplete

This strategy is supported by two independent lines of reasoning:

1. **Recency effect** (Murdoch, 1962; Liu et al., 2023): The newest entries are at the file's tail — a position of high LLM utilization. Preserving them maximizes the information the LLM can effectively use.

2. **Physical state sedimentation**: The oldest entries describe decisions whose results have already been materialized into output/ files. These entries have high redundancy with the observable world state — the reader can verify or infer them by examining the workspace. The newest entries describe decisions whose results may not yet be materialized. These entries have low redundancy — the information exists only in notes.

Trimming the oldest entries minimizes net information loss because the trimmed information has the highest redundancy with what the agent can observe directly.

### Prompt Guidance: Writing for Readers

The `write_notes` tool description and the context manager's NOTES Usage Guide guide the agent's writing from three dimensions:

**Information filtering** — only write what matters:

```
IMPORTANT: Notes have a size limit. Only write IMPORTANT entries.
Skip trivial or obvious information.
Focus on key facts that future steps MUST know —
critical discoveries, important decisions, and essential state.
When in doubt, ask: would losing this information hurt the task?
If not, skip it.
```

**Self-containedness** — don't assume shared context:

```
Write self-contained entries (future LLM calls have no memory)
```

**Traceability** — provide verification paths:

```
Include file references when relevant
Use conflicts_with when correcting earlier decisions
```

These three dimensions serve the reader-oriented design: filtering ensures notes aren't drowned in noise, self-containedness ensures the reader needs no additional context, and traceability ensures the reader can verify claims against the observable world state (output/).

---

## Part 4: Engineering Implementation

### The ReAct Loop

The task execution follows the ReAct pattern (Yao et al., 2023) — interleaved reasoning and acting:

```
for iteration in 1..max_iterations:
    1. trim_notes_md() + refresh_notes()       ← Ensure notes are current
    2. build_messages()                         ← Assemble context (fixed + window)
    3. Check if checkpoint is needed             ← Mandatory handoff documentation if due
    4. LLM call
    5. Branch on finish_reason:
       - stop:       Task complete → update notes → return success
       - tool_calls: Execute tools → append to context → continue
       - length:     Output truncated → inform agent → continue
```

Each iteration is recorded to the `interactions` table (separate from user conversation, as described in the Agent Interaction Design document). The loop is self-contained: all internal state remains within the task system, and only the final `TaskResult` crosses the boundary back to the chat system.

### Minimal Tool Selection

The agent operates with a deliberately minimal tool set:

| Tool | Information type | Why It's Necessary |
|------|-----------------|-------------------|
| **bash** | What (observe and change world state) | Covers file operations, code execution, system interaction — the agent's perception and action channel |
| **write_notes** | Why (persist reasoning for future readers) | Bridges LLM statelessness across iterations — the handoff document |
| **web_search** | What (external knowledge) | Covers real-time information (optional, only when configured) |

Bash provides the agent's interface with the world state (what). Write_notes provides the agent's interface with the continuity channel (why). These are not redundant — they serve orthogonal information types.

A critical design decision: **if a tool is unavailable, the agent doesn't know it exists.** When web search is not configured, the agent receives only bash and write_notes. Its system prompt doesn't mention web search, preventing it from attempting unavailable operations.

### Cognitive Order: Comprehend → Decide → Execute

User messages are often ambiguous or context-dependent ("change that file"). Before entering the task loop, a cognitive order pipeline (Comprehend → Decide) produces a clear execution intent called Guidance:

```
User message:  "Change that file"
Conversation:  [User: "Create a README.md", AI: "Created..."]

Comprehend → Decide pipeline produces:
  Guidance: "Modify README.md to update the installation instructions"

Guidance is the task requirement
```

This ensures the agent receives a clear, unambiguous task. The Guidance is injected into the system prompt as a self-directive. New guidance can arrive during execution via the guidance channel, enabling mid-course corrections when the user provides additional direction.

---

## Architecture Overview

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                              Task Execution                                  │
│                                                                              │
│   User Message                                                               │
│       │                                                                      │
│       ▼                                                                      │
│   ┌──────────────────────────────────────────────────────────────────────┐   │
│   │ Cognitive Order Pipeline (Comprehend → Decide)                       │   │
│   │ Ambiguous user message → Guidance (execution intent)                 │   │
│   └──────────────────────────────────────────────────────────────────────┘   │
│       │                                                                      │
│       ▼                                                                      │
│   ┌──────────────────────────────────────────────────────────────────────┐   │
│   │ TaskExecutor                                                         │   │
│   │                                                                      │   │
│   │  1. Init workspace (.meta/ + output/)                                │   │
│   │  2. Read notes.md                                                    │   │
│   │  3. Build ContextManager (fixed part + window)                       │   │
│   │  4. Assemble tools (bash + write_notes + experience + ...)           │   │
│   │  5. Create TaskLoop                                                  │   │
│   │                                                                      │   │
│   │  ┌────────────────────────────────────────────────────────────────┐  │   │
│   │  │ TaskLoop (ReAct)                                              │  │   │
│   │  │                                                                │  │   │
│   │  │  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐       │  │   │
│   │  │  │   Context    │    │     LLM     │    │    Tools    │       │  │   │
│   │  │  │   Manager    │───►│    Client    │───►│  bash (what)│       │  │   │
│   │  │  │              │    │             │    │  notes (why)│       │  │   │
│   │  │  │ Fixed:       │    │             │    │  experience │       │  │   │
│   │  │  │  - system    │    │             │    │  wake_me    │       │  │   │
│   │  │  │    (Guidance)│    │             │    └─────────────┘       │  │   │
│   │  │  │  - notes.md  │    │             │           │              │  │   │
│   │  │  │              │◄───┤             │           ▼              │  │   │
│   │  │  │ Window:      │    │             │    Tool Results          │  │   │
│   │  │  │  - last w    │    │             │           │              │  │   │
│   │  │  │  iterations  │    │             │           ▼              │  │   │
│   │  │  └─────────────┘    └─────────────┘    Next Iteration        │  │   │
│   │  │                                                                │  │   │
│   │  │  Checkpoint: distance ≥ window → write_notes only             │  │   │
│   │  │  Success:    finish=stop → update notes → return              │  │   │
│   │  │  Final:      max_iterations → save notes → return failure     │  │   │
│   │  └────────────────────────────────────────────────────────────────┘  │   │
│   │                                                                      │   │
│   └──────────────────────────────────────────────────────────────────────┘   │
│       │                                                                      │
│       ▼                                                                      │
│   TaskResult (status + result/reason + notes)                                │
│       │                                                                      │
│       ▼                                                                      │
│   Injected into Chat Context Engineering → User-friendly response            │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘
```

The information sources — Guidance (in system prompt), notes.md, output/ (via bash), and recent iterations (via window) — each serve a distinct role. No source overlaps with another, and none requires history retrieval or system compression:

| What the agent needs | Where it comes from | Information type |
|---------------------|-------------------|-----------------|
| What the user wants | Guidance in system prompt | Goal (execution intent) |
| What the world looks like now | output/ via bash | What (observable state) |
| Why things are the way they are | notes.md | Why (reader-oriented context) |
| What was just attempted | Last w iterations | What + Why (recent, unfiltered) |

---

## References

- Liu, N. F., Lin, K., Hewitt, J., et al. (2023). Lost in the middle: How language models use long contexts. *arXiv preprint arXiv:2307.03172*.
- Murdoch, B. B. (1962). The serial position effect of free recall. *Journal of Experimental Psychology, 64*(5), 482-488.
- Patterson, E. S., & Wears, R. L. (2010). Patient handoffs: Failed and successful mechanisms. In E. S. Patterson & J. Miller (Eds.), *Macrocognition Metrics and Scenarios*. Ashgate.
- Reason, J. (1990). *Human Error*. Cambridge University Press.
- Wegner, D. M. (1987). Transactive memory: A contemporary analysis of the group mind. In B. Mullen & G. R. Goethals (Eds.), *Theories of Group Behavior* (pp. 185-208). Springer.
- Yao, S., Zhao, J., Yu, D., et al. (2023). ReAct: Synergizing reasoning and acting in language models. *ICLR 2023*.
