# Memory System: Forgetting, Retrieval, and Narrative Understanding

How we design long-term memory for LLM-based agents: durable records of experienced events, use-dependent prominence, and deliberate recall when a decision needs history.

---

## The Problem

An LLM-based agent can only use what is placed in its prompt or returned by an available tool. A chat session's conversation history is a finite resource — the earlier messages may be compressed into summaries, the later ones held verbatim. When a session ends, that prompt window closes. Without persistent memory and a way to recall it, a later session cannot carry forward prior conversations, impressions, or the agent's previous actions.

The same applies within a session: each model call has no implicit shared state. The runtime can comprehend a bounded batch of unread messages together, but without explicit memory infrastructure the model must rely on whatever context the framing layer happens to provide.

This is not just a recall problem. It is a knowledge integration problem. The agent produces observations every time it processes a message — not for the user (the reply handles that), but for itself. These observations constitute its experience. Without memory, the agent throws away its experience after each invocation.

The question is not whether to build memory, but what kind of memory: a system designed around purposeful forgetting, or one that accumulates indiscriminately.

---

## Part 1: Theoretical Foundation

### The Forgetting Model: Why Memory Must Be Lossy

The intuitive approach to machine memory is preservation: record everything, then search it. This approach has two problems.

**First, the signal-to-noise ratio decays with scale.** Most conversational events are routine — greetings, small talk, transient coordination. A memory system that records everything is a system whose retrieval mechanism must filter through noise to find signal. The cost of filtering grows with the corpus; relevance degrades as unrelated events saturate the search space.

**Second, indiscriminate preservation overfits to the past.** An agent that remembers every interaction treats its entire history as equally relevant to the present. But past conversations reflect past contexts — moods, tasks, environments that may no longer apply. A memory that cannot fade is a memory that cannot adapt.

The alternative: **a forgetting-first model of prominence, not deletion.** Durable sources remain available, while their prominence in reflection is use-driven:

- An Event records an occurrence; an AgentObservation records that this particular agent encountered it. Mere membership in a conversation does not create an observation of every message.
- An observed historical message selected for Chat's longer context can gain importance. Decide's read-only recall does not currently provide this feedback.
- Importance is a use-dependent prominence signal for evidence selected during EntityProfile reflection, not a truth score or a prerequisite for historical recall.
- Unreinforced observations fade in prominence, but their rows and source Events are not deleted by decay.

This model is grounded in two well-established cognitive phenomena. **Use-dependent retention** (Anderson & Schooler, 1991) demonstrates that human memory strength follows the pattern of environmental demand — items needed more frequently are retained better, and the retention function closely tracks actual usage probability. **Forgetting as adaptive regulation** (Bjork & Bjork, 1992) reframes forgetting not as a failure of recall but as a functional mechanism: by reducing interference from outdated information, forgetting improves retrieval of currently relevant content. An agent that remembers a user's obsolete preferences is worse at serving their current needs than an agent that has let those preferences fade.

An important distinction: this is **not** recency-only ranking. Time-based decay lowers every active importance value, while qualifying use can raise it again. A repeatedly reused older observation can therefore remain prominent in EntityProfile evidence selection. An unreinforced recent one can sink in that ordering. Neither outcome deletes the historical source or establishes whether a user's stated preference is still true.

### The Two-Way Relationship with Retrieval

In many memory systems, retrieval reads and storage writes are separate. Qingqiu has one feedback path between them: when Chat selects historical message segments for its longer context, `OnRetrievalHit` updates existing observations for those messages before final assembly. Decide's `recall_*` tools remain read-only; simply finding a candidate does not strengthen it.

For an eligible observation on that Chat path:

```
importance += α × (1 - importance)         # asymptote toward 1.0
last_accessed_at = NOW()
```

The importance update uses α = 0.1 and a ten-minute scoring cooldown. Consecutive qualifying hits move importance from 0.5 to 0.55, then 0.595, then 0.6355 — each successive hit contributes less. A hit inside the cooldown updates `last_accessed_at` but does not raise importance. This saturating gain means direct hits alone approach 1.0 asymptotically; propagation can also affect the score.

A qualifying positive gain also triggers **relevance propagation** within that agent's observed messages in the same session: a fraction spreads to temporal neighbors, vector-similar observations when vectors exist (cosine > 0.8), and other messages in the session. Propagation is one-way (increase only), applies its own cooldown, and does not prove that the related messages express the same fact. This borrows the idea of spreading activation (Collins & Loftus, 1975) as a design analogy, not as validation of these particular weights.

### Importance as Retention Signal

The `importance` field is the observation's use-dependent prominence signal. An observation starts at importance=0.5. A qualifying Chat history hit or association can raise it; decay can later bring it below 0.5. The score does not determine whether the Event happened or whether it may be recalled.

**Decay**: Each maintenance pass multiplies values above 1e-6 by 0.98. Without reinforcement, an observation at 0.85 reaches ~0.46 after 30 passes; 0.55 reaches ~0.50 after five and ~0.30 after 30; 0.50 reaches ~0.27 after 30. The worker runs once at startup and then every 24 hours while running. It does not persist a last-decay date, so frequent restarts can cause more than one pass in a day.

**Boost can counter decay for active content**: Around importance 0.5, one qualifying hit adds ~0.05, roughly offsetting five maintenance passes (×0.98^5 ≈ ×0.904). This is an illustrative trajectory, not a guarantee about real usage or a fixed daily schedule. Unused observations approach the practical floor over many passes, but do not disappear from Decide recall merely because their importance fell.

**Relevance propagation also counters decay**: An observation that received a propagated delta can have importance > 0.5 even without direct retrieval. The propagation path provides a second defense against decay — semantically or temporally related observations are protected by association.

**The continuous nature matters**: Repeated decay means once-useful but now-unreinforced evidence can lose prominence in reflection. This is a soft ordering signal, not a binary gate on historical sources.

Decay is multiplicative, so importance does not normally reach exactly zero. Observations at or below 1e-6 are skipped by maintenance. The 0.1.18 recall tools do not use an `importance > 0` filter: the Event remains available under its normal access and Observation checks.

### Memory vs. Notes: Orthogonal Permanence Mechanisms

This system operates alongside the existing notes mechanism (task-execution-level shared context as described in the task execution document). They serve distinct, non-overlapping functions:

| Aspect | Notes (`notes.md`) | Memory (observations) |
|--------|-------------------|----------------------|
| **Scope** | Single task execution | Cross-session, agent-wide |
| **Purpose** | Task continuity (what-to-do-next) | Experience accumulation (what-I've-seen) |
| **Writer** | Agent during task execution (deliberate) | System (mechanical recording) |
| **Reader** | Next LLM instance in the same task | Chat context assembly or Decide recall, subject to access checks |
| **Lifetime** | One task; discarded on completion | Durable record; prominence may decay |
| **Content** | Deliberate reasoning, decisions, progress | Raw event content, loaded on demand |
| **Selection** | Agent chooses what to write | System records encountered Events; authorized recall selects sources |

Notes are a communication channel across instances of the same task — a deliberate, agent-controlled artifact. Observations are passive, system-controlled records of encountered Events; Decide can deliberately inspect permitted history but does not edit the records by recalling them. The agent writes notes to its future self within a task; the system builds memory from the agent's experience across tasks.

---

## Part 2: Architecture

### Two-Layer Design: Observation → Reflection

The memory system operates in two distinct layers:

```
Layer 1 (Observation): Mechanical recording of events
    ↓
Layer 2 (EntityProfile): LLM-generated reflection on accumulated observations
```

**Layer 1 — Observation** is fully automated and zero-LLM:
1. A message is committed with a corresponding `Event` record (`event_type=message`, `ref_id=message_id`). Embedding generation for the Event is queued separately when configured; it is not required for the Event or Observation to exist.
2. For incoming chat, Comprehend reports the exact message IDs it read. The runtime then creates this agent's Observations for those message Events and the triggering Event. A buffered, unread message does not become an experience solely because it was stored.
3. For the agent's own outgoing message, its Observation is committed with the Message and Event. Other event types gain an Observation when that agent actually processes them through Comprehend.

The observation contains no content — event content is loaded on demand via `event_id → events → (event_type, ref_id) → originating table`. This separation keeps the observation table lightweight and avoids content duplication. An Event records occurrence, not the truth of claims made in a message; an Observation records encounter, not agreement or complete understanding.

**Layer 2 — EntityProfile** is LLM-driven and triggered by density. When an agent accumulates sufficient observations pointing to a specific entity (user, agent, or session), a reflection is triggered:

1. Check density during heartbeat (performed periodically)
2. Count observations per entity direction (each observation can point to multiple: session + user + agent)
3. When count >= threshold for a direction, select top-N by importance for LLM reflection
4. Generate a fresh narrative (no prior narrative is fed to the LLM) describing the agent's understanding of the entity
5. MD5 dedup: if the evidence text is unchanged from the last generation, skip

EntityProfile is a synthesis layer — the agent forms a revisable impression, not a new objective fact. It can be loaded as background understanding or read by identity during Decide, complementing inspection of source records. The current profile is not a versioned archive of previous impressions.

### Retrieval: Source Inspection at Decision Time

Decide receives a bounded `Situation`: `Subject` describes current capacity and ongoing commitments; `Environment` lists generally available people, sessions, and resources without loading all their histories; `Matter` presents the triggering Event with direct Comprehension, or a heartbeat description. This is the current decision context, not the whole memory corpus. Chat Comprehend also includes a small observed same-session window; an agent's own recent messages can carry the background, reason, and guidance of the Actions that produced them.

For an LLM-driven decision, a short DecideLoop offers read-only `recall_*` tools. The agent can inspect observed Events and Messages, its own Actions and their siblings under a Decision, Works, Focus handoffs, and a current EntityProfile for a known entity. It can also browse sent and received Jinshu metadata and read a Jinshu description; attachment content requires a separate, deeper path. Fixed-rule decisions retain their direct path.

Known IDs and causal links permit precise navigation. When the agent needs discovery, query terms and time bounds yield bounded, paginated candidates across authorized sources. The shared term index is a replaceable lexical aid; it is not BM25 or an LLM relevance judgment. Results are checked against original records and access rules. `recall_message` can discover observed messages across accessible sessions even when the triggering session contains no clue. An empty or limited result does not establish that no relevant source exists. Event vectors exist, but Decide recall does not currently use them for semantic search. No composite `0.7 similarity + 0.2 importance + 0.1 recency` ranking runs in this path, and the `recall_*` tools do not update importance.

These sources preserve distinct kinds of evidence: Event/Message says what occurred or was said; AgentObservation says what this agent encountered; Decision and Action record why and what it chose; ActionEffect links a durably observed effect; Work and Focus handoff report continuing work and executor findings; EntityProfile is an interpretation. `Decision → Action → ActionEffect` records exact causal links where available, without claiming that a message from another person inherits the agent's private reasoning. An Action's ended state alone does not prove that its intended outcome succeeded. The same Decision can produce multiple Actions without defining an order between them.

### Relevance Propagation: Spreading Activation

When an observed historical Message gains importance through Chat's retrieval feedback, a fraction can spread to other observations of that agent in the same session:

| Propagation Rule | Factor | Rationale |
|-----------------|--------|-----------|
| Adjacent observed message (±1 position) | 0.5 | Immediate conversational neighbors may share topical continuity |
| Nearby observed message (±2 positions) | 0.2 | Weaker continuity with a two-step gap |
| Vector-similar observed message (cosine > 0.8, when vectors exist) | 0.2 | Possible topical association within this session |
| Same session | 0.15 | All events in the same session share a contextual frame |

Each target independently applies an anti-hot cooldown (10 minutes) — if a target was scored within the cooldown window, the propagation is ignored for that target. This prevents a single retrieval from triggering cascading updates that artificially inflate scores across the observation population.

The factors form a heuristic hierarchy: conversational adjacency gets the largest weight; vector similarity gets less because embeddings can produce false positives; same-session association is lightest. These links change prominence, not causal provenance or factual status. Each target is processed at most once for a source hit, so the order of these rules matters.

### Daily Maintenance: The Decay Cycle

On startup and then every 24 hours while the service runs, maintenance applies multiplicative decay to active observations:

```
importance *= 0.98   (applied to every observation with importance > 1e-6)
```

This is a uniform per-pass operation. The last pass date is not persisted, so restarts can apply additional decay within one day.

The decay factor of 0.98 was chosen to balance two concerns:

- **Too fast** (e.g., 0.90): evidence could lose prominence in reflection before it has a chance to prove useful. A message from last month's conversation might be overlooked in profile synthesis.
- **Too slow** (e.g., 0.995): decay would be negligible — an observation at 0.85 would take roughly 200 passes to drop to ~0.31. Obsolete content could remain prominent for too long.

At 0.98, the decay provides a gradient in profile evidence selection: qualifying use can counter it, while unreinforced observations gradually recede. The parameter is a design choice, not a measured cognitive constant.

Observations with importance at or below 1e-6 are excluded from decay updates. That floor does not exclude their source Events from authorized Decide recall.

The row is never deleted by decay — it remains as an archival trace. A later qualifying hit or association can raise its importance again. This is a soft fade in prominence, not a hard delete or a change in the agent's right to inspect a source.

---

## Part 3: Design Decisions and Dialectical Analysis

### Why No Automated Semantic Clustering

Many memory systems group observations into topic clusters automatically (e.g., via embedding-based clustering or topic modeling). We deliberately avoid this.

**Arguments for clustering**: Clustering could provide structure — grouping related observations for batch retrieval, summarization, or navigation. It could also serve as a compression mechanism, replacing multiple similar observations with a single cluster summary.

**Arguments against**: Automated clustering overfits to embedding space geometry. Two observations with high cosine similarity may be related (both discuss technical architecture) or may be false-positives (both use similar vocabulary about different topics). The embedding space does not encode semantic truth — it encodes distributional proximity. Relying on it for structural organization introduces a layer of misrepresentation that propagates downstream.

More fundamentally, clustering creates a maintenance burden without clear retrieval benefit. Cluster boundaries shift as new observations arrive; cluster summaries become stale; cluster labels require regeneration. Each maintenance cycle introduces churn. Qingqiu keeps its source records separate and discovers candidates at query time, currently by exact links, lexical terms, or time rather than a permanent topic cluster.

**Our choice**: Source-preserving, query-time recall alongside a separate EntityProfile synthesis path. The mechanisms are complementary: recall inspects evidence for the present decision; EntityProfile asks, "What do I currently think about this entity overall?"

### Why Event Content Is Not Cached in Observations

Observations store only a reference (`event_id`). Content is loaded on demand when the observation is retrieved. This introduces a join on every retrieval — a cost that could be avoided by caching content in the observation row.

**Why we accept this cost**: Content duplication creates a consistency problem. If a message is edited (unlikely in the current system but architecturally possible) or if content is normalized post-hoc, cached copies diverge. The event_id reference guarantees a single source of truth.

More importantly, observations belong to agents — multiple agents may each encounter the same event at different times, and some may not encounter it at all. Caching content in every observation would multiply storage for no retrieval benefit, since content is identical across agents. The join cost is a one-time query overhead; the duplication cost is permanent.

### Why a Single Importance Dimension

A single `importance` field serves as the system's sole retention signal, rather than multiple scoring dimensions (e.g., intensity, surprise, importance).

**Why we currently use one dimension**: Additional dimensions such as intensity or surprise would need definitions, update rules, and evidence that they improve reflection. Length is not importance, and embedding distance is not a reliable measure of surprise. We do not currently have enough evidence to treat either as a separate retention signal.

The marginal benefit of separate dimensions did not justify the implementation complexity. Each additional dimension requires its own update logic, decay function, and tuning parameters. Importance is partially grounded in actual Chat history use, but also in heuristic association and decay; it is therefore an engineering signal rather than a direct measure of subjective importance.

**The honest limitation**: A single dimension cannot distinguish "repeatedly injected because useful" from "repeatedly injected because similar situations or retrieval heuristics keep surfacing it." A recurring topic can gain prominence even when no occurrence is especially significant. The score is useful for ordering profile evidence; it does not settle the agent's judgment of what matters or the truth of any claim.

### Rate Limiting vs. Continuous Profile Regeneration

EntityProfile generation is rate-limited to once per 6 hours per entity direction. An alternative would be continuous regeneration — every heartbeat triggering a new profile if the density threshold is met.

**Why we rate-limit**: Continuous regeneration would create unnecessary LLM cost without proportional benefit. EntityProfile reflects patterns accumulated across many observations — a pattern does not meaningfully change hour by hour. Six hours provides a window long enough for meaningful new evidence to accumulate while keeping LLM cost bounded.

**The trade-off**: Rate limiting means the profile can lag behind the most recent observations by up to 6 hours. This is acceptable because EntityProfile is a synthesis layer, not a real-time reflection. It captures patterns, not latest messages. A 6-hour lag in pattern recognition is negligible; a 6-hour lag in a real-time response is not.

### Why No Prior Narrative in Profile Generation

When generating a new EntityProfile, we do not feed the prior narrative to the LLM. Each generation is fresh — the LLM sees only the current top evidence and forms a new impression.

**Arguments for including prior narrative**: Continuity — the old narrative could provide a starting point, reducing LLM cost and ensuring consistency across regenerations.

**Arguments against**: Including prior narrative creates a self-reinforcing loop. The LLM, presented with its own previous impression, is biased toward confirming it — even when new evidence might suggest a revision. The system would converge toward a stable but potentially inaccurate self-image, resistant to contradictory observations.

More subtly, prior narrative introduces temporal anchoring. A profile formed early in the agent's interaction with a user would persist through subsequent regenerations, each generation built on the last. The profile becomes a tradition, not a reflection.

**Our choice**: Fresh generation with MD5 dedup. The MD5 check prevents redundant LLM calls when evidence is unchanged, but when evidence changes, the generation starts from scratch. This prioritizes accuracy over continuity, accepting that profiles may shift between regenerations — which is appropriate for a system whose purpose is understanding, not storytelling.

### Identity-Driven Memory: Why the Agent Remembers as Itself

A subtler design choice runs through memory prompts: evidence of conversations uses participants' names rather than generic "Assistant" and "User" labels when names are available. The EntityProfile reflection prompt addresses the agent in the second person: "You are Alice, reflecting on your accumulated observations about Patrik."

This is not cosmetic. It is a deliberate stance on agency and embodiment.

#### The Sycophancy Problem

The labels used in a prompt can cue different conversational roles. Research on sycophancy documents cases in which language models agree with a user's stated view or tailor a response to perceived expectations (Perez et al., 2022; Sharma et al., 2023). A generic assistant frame may encourage service-oriented wording; the size and direction of that effect depend on the model and context.

Using the agent's character name ("Alice") in memory evidence instead frames a conversation among named participants: "Alice reflecting on conversations with Patrik." This may support a more coherent perspective for reflection, but it does not bypass model training, remove sycophancy, or establish that the agent has an independent inner life.

One possible design effect is the difference between a service log ("I helped the user with X") and a relational impression ("Patrik tends to approach technical problems methodically"). These are examples of framing, not evidence that naming alone improves factual accuracy. Source attribution still matters when the agent revises an impression.

#### First-Person Observation, Not Third-Person Logging

Conventional dialogue systems log conversations in third person: "User said X. Assistant replied Y." This framing positions the system as an external observer of an interaction between two parties — neither of which is the system itself. Memory in this frame is a transcript.

Our memory system uses first-person framing. The agent is not observing "User said X, Assistant said Y" — it is observing "[Patrik] said X, [Alice] said Y." Both parties are named persons in a shared conversation. The agent's own messages are tagged with its own name, not "Assistant." The person's messages are tagged with their configured name, not "User."

This design has a useful analogy to Tulving's (1972) distinction between memory for personally experienced events and general semantic knowledge. The engineering record can preserve which Events this agent encountered, while a retrieved transcript alone may show only what was said. This analogy does not establish that a model has human episodic memory.

Our design pushes the *description* toward personally situated history. AgentObservations record encounters; EntityProfile is the agent's generated interpretation of evidence — "What do I currently think about X?" — rather than an objective summary of everything that happened.

#### Evidence Labels as Embodiment Reinforcement

The EntityProfile prompt illustrates this concretely:

```
You are Alice, reflecting on your accumulated observations about Patrik.
Below are the key observations you've recorded (your messages are labeled with your name).

Key observations:
- [Patrik] I've been thinking about switching to Rust for the backend...
- [Alice] That's an interesting direction. What aspects of Rust appeal to you?
- [Patrik] Memory safety without garbage collection, mostly...
```

Every evidence line carries a name. The LLM reading this prompt is not reasoning about "user messages versus assistant messages" — it is reasoning about what Alice has learned about Patrik, and what Alice has said to Patrik. The labels anchor the agent in its identity and the other party in theirs.

This extends beyond EntityProfile. The same naming convention propagates through all memory-adjacent prompt construction — summaries, preprocessing, user state inference, task rewriting. The entire system is built on the premise that conversations happen between named individuals, not generic roles.

#### Why Not Use the Agent's Own Name for Self-Reference in Retrieval Context?

A legitimate counterargument: if the agent is always referred to by its real name, wouldn't a retrieved memory injected into context read unnaturally when the agent encounters "Alice said X" in what it perceives as its own current conversation?

This is precisely the point. The agent *should* encounter its own name when reading retrieved memories, because retrieved memories are its own past experiences. When a person recalls a past conversation, they remember themselves as a participant — "I said X" — not as a disembodied observer. The name label makes the retrieval explicit: this is something I experienced, not something that happened to someone else.

#### The Boundary: Where We Do Use "assistant"

This design is consistent but not total. The LLM API protocol layer uses `role: "assistant"` because this is an OpenAI-compatible API requirement — the model's training expects this token. But the role label never reaches the agent's prompt content. The protocol layer's "assistant" is a transport encoding; it does not define the agent's identity.

Similarly, the `messages` table uses `role = 2` (integer enum for assistant) for storage efficiency. This is a database encoding, not a semantic label. The agent never sees it.

---

## Part 4: System Integration

### Ingestion Hooks

Memory ingestion separates durable occurrence from asynchronous vectorization:

- **Incoming messages**: A Message Event is persisted when the message is created. Embedding generation is queued separately. The receiving agent's Observations are written for the exact messages Comprehend consumed and the triggering Event; messages still buffered or unread are not treated as encountered.
- **Agent messages**: The runtime commits its outgoing Message, Event, and its own Observation together. Embedding generation remains a separate background step.

Vectorization failure does not erase a committed Event or retroactively create an Observation. The term index and Event vectors aid candidate search where their paths use them; the source record remains authoritative.

### Retrieval Integration Points

Memory retrieval now has two distinct integrations:

1. **Chat history feedback** (`OnRetrievalHit`): When Chat selects historical Message segments for its longer context, their existing Observations can receive use-dependent boosts and association propagation. This is not a general boost for all prompt contents; the hook runs before final prompt assembly.

2. **Decide recall** (`recall_*`): A bounded tool loop lets the agent inspect authorized records on demand. It supports exact IDs, causal navigation, time-bounded browsing, and lexical discovery across several source types. This path is read-only; it does not use a composite semantic score or write retrieval boosts. EntityProfile can be read by a known person or session ID.

### Heartbeat-Driven Density Check

EntityProfile density checks are driven periodically by the agent heartbeat system. The runtime calls `CheckProfileDensity`, which scans observations and triggers profile generation for eligible entity directions.

This integration is intentionally lightweight — density checks are read-only scans; profile generation is spawned asynchronously. The heartbeat continues uninterrupted regardless of profile generation outcome.

---

## References

- Anderson, J. R., & Schooler, L. J. (1991). Reflections of the environment in memory. *Psychological Science, 2*(6), 396–408.
- Bjork, R. A., & Bjork, E. L. (1992). A new theory of disuse and an old theory of stimulus fluctuation. In A. Healy, S. Kosslyn, & R. Shiffrin (Eds.), *From Learning Processes to Cognitive Processes: Essays in Honor of William K. Estes, Vol. 2* (pp. 35–67). Erlbaum.
- Collins, A. M., & Loftus, E. F. (1975). A spreading-activation theory of semantic processing. *Psychological Review, 82*(6), 407–428.
- Perez, E., Ringer, S., Lukošiūtė, K., et al. (2022). Discovering language model behaviors with model-written evaluations. *arXiv preprint arXiv:2212.09251*.
- Sharma, M., Tong, M., Korbak, T., et al. (2023). Towards understanding sycophancy in language models. *arXiv preprint arXiv:2310.13548*.
- Tulving, E. (1972). Episodic and semantic memory. In E. Tulving & W. Donaldson (Eds.), *Organization of Memory* (pp. 381–403). Academic Press.
- Wegner, D. M. (1987). Transactive memory: A contemporary analysis of the group mind. In B. Mullen & G. R. Goethals (Eds.), *Theories of Group Behavior* (pp. 185–208). Springer.
