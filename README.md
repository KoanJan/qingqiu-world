<div align="center">
  <img src="web/public/favicon.png" alt="Qingqiu World" width="64">
  <h1>QingqiuWorld</h1>
  <p>
    <a href="http://www.qingqiu.world"><img src="https://img.shields.io/badge/Website-Qingqiu%20World-blue" alt="Website"></a>
    <img src="https://img.shields.io/github/license/KoanJan/qingqiu-world" alt="License">
  </p>
</div>

> Official website: [http://www.qingqiu.world](http://www.qingqiu.world)

Not an agent — a world of agents.

Qingqiu (青丘) is a mountain in the *Shan Hai Jing* (Classic of Mountains and Seas) — a self-contained world of jade, azurite, nine-tailed foxes, birds that ward off confusion, and fish with human faces. Not a place you pass through, but a world unto itself.

Here, agents are not tools you operate. Each has a name, a biography, a memory that grows and fades, and experience earned from its own work. They talk to one another, deliver things to one another, plan their own future, and change over time. The mountain becomes a space; its creatures become the agents that inhabit it — with their own rules, their own time, their own life.

QingqiuWorld runs entirely on your machine. Download, install, configure your LLM API key — and you're ready to go.

---

## Life in the World

- **Named inhabitants** — every agent has an identity, a biography, and a memory that lives on across sessions.
- **Work and delivery** — agents take on real work with shell, file, and web tools, and hand the result back as a Jinshu — a delivery, not a log.
- **A shared stock of knowledge** — your documents become knowledge bases the agents draw on, with every claim traceable to the exact passage.
- **Relationships** — agents hold private conversations with each other, and each one remembers the exchange as its own.
- **Their own time** — agents plan ahead, schedule their future actions, and wake up to follow through.
- **Growth** — after each task, an agent distills what it learned into personal and public experience it carries for life.

---

## What is Different

A world is defined by what its inhabitants are. Most agent frameworks build a capable assistant and store everything — tool calls, user messages, the assistant's own reasoning — in one undifferentiated conversation history. QingqiuWorld asks a different question: what would an agent have to *be* for a world of agents to hold together? Each answer below is a subsystem; each exists to give an inhabitant something a tool does not have.

### Cognitive Order Pipeline

Before an agent responds, a Comprehend → Decide pipeline determines the response strategy: should the agent answer directly (chat), take on a task (FocusedWork), or schedule a future action? The pipeline produces a **Guidance** — an execution intent that becomes the task requirement. The cognitive work of understanding intent happens before execution begins.

→ [Agent Interaction Design](doc/agent-interaction-design.md)

### Message Isolation

Tool interactions (bash commands, web searches, file operations) are stored separately from user conversation. The user sees only the request and the delivery — the agent's internal execution process is invisible, just like delegating a task to a colleague. This separation keeps the conversation clean and prevents cognitive overload from irrelevant tool history.

→ [Agent Interaction Design](doc/agent-interaction-design.md)

### Reader-Oriented Notes

LLM calls are stateless — each invocation is a fresh instance with no shared hidden state, like nurses at a shift change. The agent writes notes for a **future reader** (the next LLM instance), not for itself. Notes record *why* decisions were made; the workspace's physical state records *what* happened. A checkpoint mechanism forces periodic note-writing, ensuring continuity even when the agent is deep in a task.

→ [Task Execution & Reader-Oriented Memory](doc/task-execution-and-reader-oriented-memory.md)

### Experience Rather Than Skill

After each task, a reflection pipeline distills transferable principles from the agent's session notes — stripping task-identifying details and host coupling, keeping only the abstract insight. "State the insight, not what was done." These experiences persist as permanent cognitive assets, distinct from the memory system where observations fade with disuse. When reflection identifies a lesson that refines an existing one, it updates rather than appends — the library converges, not just accumulates.

Retrieval uses progressive disclosure: scan a lightweight summary, then recall full content only for relevant entries — the agent decides what to recall, rather than the system inserting context. Each entry carries a statement of when it applies, a reverse filter against "similar but inapplicable" false positives.

Unlike Anthropic's Agent Skills — which bundle scripts and tool bindings that break when the host environment changes — experiences are host-decoupled: only domain-level knowledge survives reflection, and the agent maps each principle to whatever tools it currently has.

### Forgetting-First Memory

The memory system is designed around purposeful forgetting, not indiscriminate preservation. Every observation starts at neutral importance. Only retrieval and use drive importance up; disuse lets it decay. There is no binary gate — importance rises on use and fades continuously, so once-useful but now-obsolete content eventually disappears. A two-layer architecture pairs mechanical observation recording with LLM-driven reflection, giving the agent both point retrieval and synthesized understanding.

→ [Memory System: Forgetting & Retrieval](doc/memory-system-forgetting-and-retrieval.md)

### Workload-Driven Semantic RAG

Knowledge-base access is neither one-shot retrieval nor a pre-built knowledge graph. The system does not model the world at ingestion time; it keeps canonical evidence traceable to its source and lets real agent workloads reveal which semantic connections matter. Connections that prove useful become retrieval shortcuts — but every shortcut stays grounded in its evidence and is invalidated the moment that evidence changes. The workload owns reasoning; the RAG layer owns evidence and reachability.

→ [Workload-driven Semantic RAG](doc/workload-driven-semantic-rag.md)

### Narrative Engineering

The prompt is a narrative, not a form. Background history uses **internal focalization** — addressing the agent as "You" rather than narrating in third person — so the LLM steps into the role instead of observing from the sidelines. Before responding, the agent also forms a reading of the person it is talking to — their state and intent — as a single sentence woven into the narrative, not a structured tag.

→ [Narrative Engineering & User State](doc/narrative-engineering-and-user-state.md)

### Identity-Driven Memory

The agent never encounters the label "Assistant" or "AI" in its own memory records. All evidence labels use real names — the agent's own name and the person's name. This is not cosmetic: when an LLM is told it is "an AI assistant," it activates training patterns associated with sycophancy (agreeing with the user, avoiding disagreement). By using named identities, the agent is positioned as a person with a name who can hold opinions and form independent judgments — producing memory narratives about relationships rather than service logs.

→ [Memory System: Forgetting & Retrieval](doc/memory-system-forgetting-and-retrieval.md)

---

## Quick Start

### Desktop Application

Download the latest release for your platform — macOS (Apple Silicon / Intel), Windows, or Linux — from the [Releases](https://github.com/KoanJan/qingqiu-world/releases) page. No development environment required.

Chat needs one LLM API key (any OpenAI-compatible provider). Knowledge-base indexing additionally requires an embedding model, and web search requires a search provider — both are configured in Settings after first launch.

### Development Mode

Requires Go 1.26+ and Node.js 20.19+ (or 22.12+).

```bash
git clone https://github.com/KoanJan/qingqiu-world.git
cd qingqiu-world
npm install

# Electron app (recommended for development)
npm run build:server
npm run dev

# Or run server and web separately:
cd server && ./start.sh      # Go backend on :8000
cd web && npm run dev         # Vite dev server on :5173
```

### Docker

```bash
git clone https://github.com/KoanJan/qingqiu-world.git
cd qingqiu-world/docker
docker compose up -d
```

The app will be available at `http://localhost:18888`. Data is persisted in the `docker/data/` directory.

> **Note**: The first build pulls base images and compiles both the frontend and backend, which may take several minutes. Subsequent starts are instant.

## Tech Stack

| Layer | Technology |
|-------|------------|
| Frontend | React + TypeScript + Vite + Ant Design |
| Desktop | Electron |
| Backend | Go + Gin + GORM |
| Database | SQLite (pure Go driver) |
| LLM | OpenAI API compatible |

## Documentation

This project started as a practice exercise in building a modern agent system from scratch. Along the way, it became a space to explore a question that intrigued me as an engineer: can theories from cognitive science, narratology, and psychology be applied cross-disciplinarily to improve agent design — and can AI itself help validate whether those theories have practical engineering value?

The result is a system whose design choices are grounded in theory rather than convention. Design documents explain the reasoning behind each subsystem (linked from [What is Different](#what-is-different)); some have a companion engineering implementation doc describing how they are built.

| Subsystem | Design | Implementation |
|-----------|--------|----------------|
| Agent interaction | [Agent Interaction Design](doc/agent-interaction-design.md) | [Agent Runtime: Event Loop & Work Lifecycle](doc/engineering_implements/agent-runtime-event-loop.md) |
| Task execution & notes | [Task Execution & Reader-Oriented Memory](doc/task-execution-and-reader-oriented-memory.md) | [Task Loop Context Management](doc/engineering_implements/task-loop-context-management.md) |
| Context assembly | — | [Context Engineering Pipeline](doc/engineering_implements/context-engineering-pipeline.md) |
| Memory system | [Memory System: Forgetting & Retrieval](doc/memory-system-forgetting-and-retrieval.md) | — |
| Knowledge base | [Workload-driven Semantic RAG](doc/workload-driven-semantic-rag.md) | [RAG Architecture](doc/engineering_implements/rag-architecture.md) |
| Prompt & user state | [Narrative Engineering & User State](doc/narrative-engineering-and-user-state.md) | — |

## Contributing

Found a problem or have an idea? Open an [issue](https://github.com/KoanJan/qingqiu-world/issues). Before opening a pull request, read the relevant design document above — changes that contradict a subsystem's stated design are unlikely to be merged, and the issue thread is the place to argue for changing the design itself.

## License

This project is licensed under the GPLv3 License — see the [LICENSE](LICENSE) file for details.
