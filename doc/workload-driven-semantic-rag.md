# Workload-driven Semantic RAG

Why this project's knowledge-base system is not just naive RAG, and why it is also not a full knowledge graph.

---

## The Problem

Naive RAG treats knowledge access as a one-shot retrieval problem:

```text
question → similar chunks → answer
```

This works when the question and the needed evidence are close in wording or embedding space. It becomes weak when the answer depends on indirect connections:

- one document defines a concept while another document uses it;
- the user's wording names an effect, but the evidence describes a cause;
- the relevant document is not directly similar to the original query;
- a task requires several searches before the useful evidence becomes visible;
- an agent repeatedly discovers the same connection, but the retrieval system forgets it every time.

The conventional answer is to build a knowledge graph:

```text
documents → entities → relations → graph traversal
```

But a full ontology-driven graph is too heavy for this project. It asks the system to decide too early what the world contains, what entity types exist, which relation schema is correct, and which extracted facts deserve to become part of a graph. That is a poor fit for a local-first agent whose knowledge bases are open-ended, user-specific, and constantly changing.

Workload-driven Semantic RAG takes a different position:

```text
Do not pre-model the world.
Keep evidence canonical.
Let real agent workloads reveal useful semantic structure.
Materialize only the relations that are grounded, useful, and invalidatable.
```

The goal is not to build a complete knowledge graph. The goal is to make evidence more reachable.

---

## Core Idea

The system is built around one boundary:

```text
The workload owns reasoning.
RAG owns evidence and semantic structure.
```

A workload is the agent's task context: it decides what it is trying to answer, why a search is needed, whether the evidence is enough, and whether another search should happen.

The RAG system does not own that loop. It does not decide task completion. It does not produce final conclusions. It provides the substrate that a workload can reason over:

- evidence retrieval;
- evidence provenance;
- context expansion;
- evidence handles;
- relation-based semantic expansion;
- maintenance of useful semantic shortcuts.

This separation prevents the knowledge-base layer from becoming a second hidden agent.

---

## The Three-Layer Model

Workload-driven Semantic RAG has three conceptual layers:

```text
Canonical Evidence
  ↓
Workload Usage
  ↓
Adaptive Semantic Structure
```

### Canonical Evidence

Canonical evidence is the source of truth.

It is the part of the system that says:

> This piece of text exists in the knowledge base, belongs to the current document representation, and can be traced back to its source.

Evidence is not whatever the model says. It is not the user's query. It is not the agent's private reasoning. It is not a relation label. It is source-backed content that can be inspected again.

This layer must be conservative because every later semantic structure depends on it.

### Workload Usage

Workload usage is the record of how the agent actually uses the knowledge base.

It answers:

> What did the agent search for, why did it search, and which evidence did it see?

This layer is important because semantic structure should not be built from every possible phrase in every document. It should emerge from real use. If no workload ever needs a connection, materializing it adds cost and noise.

Usage is not evidence. It is context about evidence discovery.

### Adaptive Semantic Structure

Adaptive semantic structure is the sparse layer of reusable semantic shortcuts.

It answers:

> Which evidence-grounded connections have become useful enough to help future retrieval?

This layer is derived. It is not allowed to outrank or replace canonical evidence. A relation is only useful because it points back to evidence.

---

## Evidence First

The most important rule is:

```text
Evidence precedes structure.
```

The system should first preserve reliable evidence. Only after evidence is retrieved, used, and connected in real workloads should the system consider materializing semantic structure.

This reverses the usual ontology-first approach:

```text
Ontology-first:
schema → extraction → graph → query

Workload-driven:
evidence → workload usage → grounded structure → better retrieval
```

The second path is slower to grow, but better suited to a local, evolving knowledge base. It avoids treating early schema decisions as permanent truths.

---

## What Counts as Evidence

Evidence is source-backed content that can support an answer or ground a relation.

Evidence must be:

- visible to the current caller;
- part of the current representation of a document;
- traceable to its source;
- readable again when needed.

The following are not evidence:

- the user's question;
- the search reason;
- the agent's final answer;
- the LLM's prior knowledge;
- a relation candidate;
- a relation label;
- stale document content;
- inaccessible knowledge-base content.

This boundary matters because a RAG system becomes unreliable the moment it treats its own interpretations as evidence.

---

## Evidence Handles

An evidence handle is a reference to evidence, not the evidence itself.

It exists because a workload often needs to carry evidence through several steps without copying the full text each time. A handle lets the agent say:

> I may need this evidence later. Keep a stable way to read it again.

This is especially important when tool output must be shortened. Body text may be shortened; evidence references should not be lost.

A handle does not prove anything by itself. It must be checked again when used, because access, document state, and evidence validity can change.

---

## Query and Reason

The system separates two things:

```text
query  = what to retrieve
reason = why this retrieval is being attempted
```

The query drives retrieval. The reason preserves the workload's intent.

This distinction is valuable because the same query can serve different purposes. But the reason is not evidence. It is not chain-of-thought. It cannot prove a relation. It cannot widen authorization.

Reason is allowed to guide later analysis. It is not allowed to become truth.

---

## Retrieval and Expansion

Retrieval begins with evidence that is directly similar to the query. These direct hits are anchors.

The system may then expand around those anchors in two ways.

### Context Expansion

Context expansion adds nearby source context.

Its purpose is to compensate for retrieval unit boundaries. A relevant passage may need the preceding paragraph, heading, or neighboring section to be understandable.

Context expansion is not reasoning. It is still evidence retrieval.

### Relation Expansion

Relation expansion follows already-grounded semantic shortcuts to find related evidence.

Its purpose is to reach evidence that may not be directly similar to the query but has an evidence-supported connection to something that was retrieved.

Relation expansion is also not reasoning. It does not decide that the task is solved. It only widens the evidence set available to the workload.

---

## Relation as Shortcut, Not Truth

A relation is a semantic shortcut between two meaningful labels, grounded in evidence.

It should be understood as:

```text
This connection has been supported by evidence before,
so it may help future retrieval find related evidence.
```

It should not be understood as:

```text
This is now an independent fact that can replace the source evidence.
```

The relation exists to improve reachability. If it cannot point back to evidence, it should not participate in retrieval.

This is the main difference between this design and a traditional knowledge graph. The graph-like structure is not the knowledge base's truth layer. It is a retrieval accelerator.

---

## Candidate, Grounding, Admission

The semantic layer is built in three steps:

```text
candidate → grounding → admission
```

### Candidate

A candidate is a possible relation.

The LLM may propose candidates because LLMs are good at noticing patterns in text. But candidates are untrusted. A candidate is only a hypothesis about useful structure.

### Grounding

Grounding checks whether the candidate is actually supported by active evidence.

This is the hard boundary:

```text
The LLM proposes.
The program verifies.
```

Grounding should require that the relation can be traced to evidence and that the supporting text is actually present.

### Admission

Admission is the point where a grounded candidate becomes part of the reusable semantic layer.

Admission should be conservative. A relation that is unsupported, stale, inaccessible, or too vague should not become a shortcut.

---

## Entity Boundary

An entity is a reusable label identity that can serve as one side of a relation.

It is not every noun. It is not every keyword. It is not a global ontology node.

Good entity candidates are:

- named things;
- stable project or domain concepts;
- systems, modules, features, documents, versions, protocols, standards;
- concepts that appear in evidence and can be reused across relations;
- labels that help future retrieval move from one evidence area to another.

Poor entity candidates are:

- temporary phrases such as "this issue" or "the above problem";
- the user's query text;
- the search reason;
- relation predicates;
- evidence metadata;
- generic words with no stable meaning;
- unsupported model summaries;
- model common sense not present in evidence.

The entity layer exists only to make relations reusable. If an entity does not help relation reuse or retrieval reachability, it is probably noise.

Entities are also scoped. The same label in different knowledge scopes should not automatically become the same entity. This prevents accidental merging across unrelated or unauthorized knowledge spaces.

---

## Predicate Boundary

A predicate is the semantic label of a relation.

It should be short, open-vocabulary, and evidence-supported.

The system should not require a fixed predicate ontology too early. Open vocabulary is important because local knowledge bases may contain project-specific relationships that no general schema anticipates.

At the same time, predicates should be normalized enough to avoid trivial duplication:

```text
depends on
requires
relies on
```

These may need to converge when they express the same useful relation. But this normalization must remain lightweight. Over-normalization would recreate the ontology problem the system is trying to avoid.

---

## Why Workload-Driven

The system does not extract all relations at ingestion time because most possible relations are useless.

A document may contain many names, concepts, and statements. Only a small fraction will matter to future tasks. The workload is the best signal for usefulness because it shows what the agent actually tried to understand.

Workload-driven structure has three advantages:

1. It is sparse. Only used connections tend to become structure.
2. It is adaptive. New tasks can reveal new useful connections.
3. It is practical. The system spends semantic maintenance effort where retrieval has already shown demand.

The tradeoff is that the semantic layer grows over time. It is not complete on day one. That is intentional.

---

## Why Not Let the Agent Directly Write Relations?

An agent may believe a relation is useful while solving a task. But direct writes from the agent would blur the boundary between reasoning and evidence.

The safer pattern is:

```text
agent uses KB → system records usage → analyzer proposes → grounding verifies → relation is admitted
```

This keeps the agent's reasoning valuable without letting it directly overwrite the knowledge structure.

The agent influences the semantic layer through what it searches and what evidence it uses, not by declaring facts.

---

## Invalidation

Derived structure must be easier to remove than evidence.

If source evidence changes, disappears, or becomes inaccessible, relations supported by that evidence must stop affecting retrieval.

This is why relations must remain connected to evidence. Without that connection, the system cannot know which semantic shortcuts are stale.

The rule is:

```text
Evidence can invalidate relations.
Relations cannot validate evidence.
```

---

## Compared with Other RAG Patterns

This section compares architecture patterns, not first-hop retrieval algorithms. The choice of lexical, vector, or hybrid recall affects candidate quality, but it does not by itself define a RAG architecture.

The architectural question is different:

> Where does semantic structure come from, how much authority does it have, and whether it persists across workloads?

| Pattern | Core idea | Strength | Main risk | Difference here |
|---|---|---|---|---|
| Naive RAG | Treat each query as an isolated retrieval-and-answer step. | Simple and effective for direct questions. | Misses indirect evidence and forgets useful connections after each query. | We keep direct retrieval as the anchor step, but do not stop at one-shot recall. |
| Ontology-driven RAG | Define entity and relation schemas before extraction. | Strong structure when the domain is stable and well-modeled. | Premature schema decisions, high maintenance cost, poor fit for open local knowledge. | We avoid predefined ontology and let useful predicates/entities emerge from evidence and workload usage. |
| GraphRAG / knowledge-graph RAG | Build a graph from documents, then retrieve through graph structure. | Powerful global structure and multi-hop traversal. | Expensive indexing, noisy extraction, and risk of treating derived graph facts as truth. | Our relation layer is sparse, evidence-grounded, invalidatable, and acts as a retrieval shortcut rather than a truth layer. |
| Query-time multi-hop RAG | Let the agent or retriever perform multiple searches during the query. | Flexible and can adapt to the current question. | Repeats the same discoveries and may not preserve useful paths for later. | The workload still owns multi-step search, but useful grounded discoveries can be materialized for future retrieval. |
| Semantic cache | Cache previous query/answer or query/result pairs. | Fast reuse for similar future requests. | Reuses outputs rather than understanding evidence connections; can become stale or over-specific. | We cache evidence-grounded relations, not final answers. Future workloads still read evidence and reason for themselves. |
| Ecphory-style associative RAG | Use entity cues and associative search to recall related evidence dynamically. | Stronger reachability without requiring a complete upfront graph. | If associations stay purely query-time, useful relations may not accumulate; if over-materialized, noise grows. | We share the associative spirit, but materialize only grounded relations that real workloads reveal as useful. |

The intended architectural position is:

```text
more adaptive than one-shot retrieval RAG
lighter than ontology / full GraphRAG
more persistent than pure query-time association
more evidence-governed than semantic answer caching
```

This is why the system is not trying to win by having the biggest graph. It is trying to preserve the smallest semantic structure that reliably improves evidence discovery.

---

## What This System Is Not

Workload-driven Semantic RAG is not:

- a complete ontology;
- a full knowledge graph;
- a graph database requirement;
- a theorem prover;
- a hidden multi-hop answer loop;
- a model-memory system;
- a place to store agent opinions;
- a way to turn every keyword into an entity;
- a replacement for source evidence.

It is a retrieval architecture:

```text
evidence-first retrieval
  + workload-shaped semantic memory
  + grounded shortcuts for future evidence discovery
```

---

## Design Tests

A future change is aligned with this design if it preserves these tests:

1. Can every relation point back to evidence?
2. Can stale evidence remove a relation from retrieval?
3. Can metadata survive when evidence text is shortened?
4. Can the workload issue another search instead of relying on a hidden RAG loop?
5. Are query and reason treated as context rather than proof?
6. Are entities scoped rather than globally merged?
7. Is the LLM limited to proposing structure rather than deciding truth?
8. Does relation expansion return evidence rather than conclusions?

If the answer to any of these becomes "no", the system is drifting away from Workload-driven Semantic RAG.

---

## Summary

Workload-driven Semantic RAG is a middle path between naive RAG and full knowledge graphs.

Naive RAG forgets useful semantic connections after each retrieval. Full knowledge graphs require too much upfront modeling and risk turning derived structure into false authority.

This design keeps evidence canonical, lets agent workloads reveal useful connections, and materializes only grounded semantic shortcuts that improve future retrieval.

The system's long-term intelligence should come not from pretending to understand the whole knowledge base in advance, but from remembering which evidence-grounded paths have repeatedly helped real work.
