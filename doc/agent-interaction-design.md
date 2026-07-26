# Agent Interaction Design: Minimal Tools & Message Isolation

How we design the agent's interaction with the world while maintaining a clean boundary with user conversation.

---

## The Problem

When an agent executes a task, it needs to interact with the external world—reading files, running commands, searching the web. But a fundamental question arises: **where do these interactions belong in the conversation?**

Current mainstream frameworks treat all messages uniformly:

```
Message 1: User "Implement feature A"
Message 2: Assistant [tool_call: read_file]
Message 3: Tool "file contents..."
Message 4: Assistant [tool_call: edit_file]
Message 5: Tool "success"
Message 6: Assistant "Feature A is done"
Message 7: User "What's the weather today?"
```

When the user asks Message 7, the system passes all previous messages to the LLM. But Messages 2-5 are internal operations—they have nothing to do with the weather question. This creates two problems:

1. **Cognitive overload**: The LLM processes irrelevant information
2. **Token waste**: Every unnecessary message costs money and latency

The deeper issue: **tool calls are fundamentally interactions between the agent and the world, not between the agent and the user.** Mixing them conflates two distinct interaction boundaries.

---

## Part 1: Theoretical Foundation

### The Delegation Model

Consider how humans delegate tasks. When you ask a colleague to "check the earliest flight to Shanghai":

1. **You make a request**: "Check the earliest flight to Shanghai."
2. **They acknowledge**: "Okay, I'll check."
3. **(Silent period)** They work—calling airlines, checking websites, comparing options. These activities are invisible to you.
4. **They deliver**: "The earliest is MU5101, departing 7:00 AM tomorrow."

The intermediate steps—their search paths, the websites they visited, the calculations they made—are **isolated**. This isolation isn't a technical limitation; it's a social protocol that protects your attention and grants them autonomy.

**Key insight**: The agent's execution process should be invisible to the user. Only the request and the delivery cross the user-agent boundary.

### Interaction Boundary Theory

From the Theory of Agents framework (Wang et al., 2025), agents operate across two distinct boundaries:

| Boundary | Interaction Type | Purpose |
|----------|-----------------|---------|
| **Agent ↔ User** | Communication | Understand intent, deliver results |
| **Agent ↔ World** | Execution | Perform operations, gather information |

Tool calls belong to the Agent ↔ World boundary. They are the agent's means of extending beyond its parametric knowledge, not part of the conversation with the user.

This distinction has cognitive science backing. The agent maintains two separate models:

1. **User Mental Model**: Understanding the user's goals, preferences, context
2. **World Model**: Understanding the environment's structure, rules, and state

Conflating these models—putting tool calls in the user conversation—creates a confused cognitive architecture.

### The ReAct Pattern

ReAct (Reasoning + Acting) provides the execution pattern:

```
Thought → Action → Observation → Thought → ...
```

From the interaction boundary perspective:

- **Thought**: Internal cognitive process (invisible to user)
- **Action**: Agent-world interaction (invisible to user)
- **Observation**: World feedback to agent (invisible to user)

The user only sees the final output derived from the last Thought. The Action-Observation loop is the agent's internal machinery.

---

## Part 2: Minimal Tool Selection

### Philosophical Foundation

When designing an agent's tool set, the fundamental question is:

> **What irreducible meta-capabilities must an agent possess to interact with the real world?**

Bash and web search are not arbitrary choices—they are instantiations of deeper functional necessities.

#### The Four Irreducible Capabilities

Any agent—biological or digital—that acts upon the world must satisfy a minimal loop:

```
Perception → Judgment → Action → Feedback → Correction
```

This requires four fundamental capabilities:

| Capability | Philosophical Basis | What It Enables |
|------------|---------------------|-----------------|
| **State Reading** | Exteroception | Bringing external world states into the agent's cognitive system |
| **State Changing** | Actuation | Converting internal decisions into external modifications |
| **Action-Feedback Loop** | Reafference | Distinguishing "world changed because of me" from "world changed on its own" |
| **Proprioception** | Extended Mind (Clark & Chalmers) | Treating tool states as part of the agent's own cognitive process |

**Without state reading**: The agent is blind to the world, unable to form judgments about external reality.

**Without state changing**: The agent is a pure "thought experimenter," producing only language output with no causal impact.

**Without feedback loops**: The agent cannot confirm whether actions succeeded, making plan correction impossible.

**Without proprioception**: The agent cannot maintain a coherent sense of "what I'm currently doing" across tool invocations.

#### Why Bash Covers All Four

| Capability | Bash Implementation |
|------------|---------------------|
| State Reading | `cat`, `ls`, `grep` — read files, list directories, search content |
| State Changing | `echo >`, `rm`, `mkdir` — write, delete, create |
| Action-Feedback | Exit codes (`$?`), stdout/stderr — confirm success or failure |
| Proprioception | Working directory, environment variables, open file descriptors — the agent's "current body" |

Bash is not just a command-line interface—it is a complete perception-action system for the digital world.

#### Why Web Search Complements Bash

Web search provides what Bash cannot: **access to real-time, external knowledge**. The LLM's parametric knowledge has a cutoff date and cannot answer questions about current events, recent documentation, or live data.

| Capability | Web Search Implementation |
|------------|---------------------------|
| State Reading | Retrieve current information from the internet |
| State Changing | None (read-only) |
| Action-Feedback | Search results, relevance indicators |
| Proprioception | None (stateless) |

Web search is a specialized tool for one meta-capability: reading the state of the external knowledge ecosystem.

#### What We Considered But Didn't Add

| Tool | Why Not Added |
|------|---------------|
| **Database connector** | Can be invoked via bash (`mysql -e`, `psql -c`) |
| **API caller** | Can be invoked via bash (`curl`, `wget`) |
| **Code interpreter** | Bash can execute Python, Node, etc. |
| **File editor** | Bash can use `sed`, `awk`, or write directly |

The principle: **a tool must earn its place by being irreplaceable.** Specialized tools that can be composed from bash commands should not exist as separate primitives.

### The Minimal Tool Set Principle

We deliberately keep the agent's tool set small. Each tool must earn its place by being irreplaceable — specialized tools that can be composed from bash commands should not exist as separate primitives.

| Tool | Capability | Why It's Necessary |
|------|------------|---------------------|
| **bash** | Execute shell commands | Covers file operations, code execution, system interaction |
| **write_notes** | Persist reasoning for future LLM instances | Bridges LLM statelessness across iterations (see task execution doc) |
| **scan_my_experience** | Search past experiences by keyword | Retrieves lessons learned from prior tasks |
| **recall_my_experience** | Read full experience content | Loads specific experience detail by ID |
| **wake_me_when** | Schedule future trigger | Enables time-based agent activation |
| **web_search** | Search the internet | Covers real-time information (optional, only when configured) |

This minimalism is intentional. Each additional tool:

- Increases the agent's decision complexity
- Expands the attack surface for errors
- Dilutes the agent's focus

### Tool Availability and Agent Awareness

A critical design decision: **if a tool is unavailable, the agent shouldn't know it exists.**

```
tools = [BashTool, WriteNotesTool, WakeMeWhenTool, ScanExperienceTool, RecallExperienceTool]

if searchConfig != nil && searchConfig.IsAvailable():
    tools.append(WebSearchTool)

return tools
```

When search is disabled, the agent never receives the web search tool. Its system prompt doesn't mention web search. This prevents the agent from attempting unavailable operations and failing.

### Workspace Isolation

The bash tool is confined to a workspace directory:

```
~/QingqiuWorldData/
└── workspace/
    └── {agent_id}/
        ├── {session_id}/          # Session's workspace
        │   ├── .meta/             # System-managed files
        │   └── output/            # Agent's working directory
        └── {session_id}/
            └── ...
```

This provides:

1. **Security**: Commands cannot access paths outside the workspace
2. **Isolation**: Each session's files are independent
3. **Continuity**: The same workspace persists across multiple deliveries in a session
4. **Multi-agent readiness**: The agent_id layer prepares for future multi-agent isolation (Actor model)

Path resolution is consolidated in a dedicated workspace package (`workspace.GetWorkspacePath`, `workspace.GetMetaDir`, `workspace.GetOutputDir`), eliminating scattered path concatenation logic across packages.

Path traversal detection ensures commands like `cd ..` or `/etc/passwd` are blocked.

---

## Part 3: Message Isolation Architecture

### The Separation Principle

User conversation and tool interactions are stored separately:

```
Session (id=1):
  ├─ Messages (User Conversation):
  │    ├─ msg_1: user "Implement feature A"
  │    ├─ msg_2: agent "Feature A is done"
  │    ├─ msg_3: user "Modify it slightly"
  │    └─ msg_4: agent "Modified"
  │
  └─ Interactions (Tool Interactions):
       ├─ {user_msg_id: msg_1, agent_msg_id: msg_2, iteration: 1, type: 1, data: {...}}
       ├─ {user_msg_id: msg_1, agent_msg_id: msg_2, iteration: 1, type: 2, data: {...}}
       └─ ...
```

The `messages` table contains only user-agent dialogue. The `interactions` table contains the agent's internal execution records.

### Interaction Record Structure

Each interaction captures one step of the ReAct loop:

| Field | Description |
|-------|-------------|
| `session_id` | Session for cross-session queries |
| `user_msg_id` | User message that triggered execution |
| `agent_msg_id` | Agent message that delivers the result |
| `iteration` | Step number in the execution |
| `type` | 1=request (to LLM), 2=response (from LLM) |
| `data` | JSON payload |

The `type` field uses the **agent's perspective**:

- `type=1`: What the agent received (messages sent to LLM)
- `type=2`: What the agent decided (LLM output with thoughts and tool calls)

This perspective is crucial. We record what the agent "saw" and "decided," not what an external observer would note (exit codes, stdout). The world's feedback (tool results) becomes part of the next request's input.

### Interaction Visibility

The frontend determines whether a message has interactions by querying the interactions table directly. An activity view API aggregates raw interaction records into a human-readable execution timeline, allowing the frontend to toggle between chat and activity views per message. Availability is determined by the presence of interaction records via API.

---

## Part 4: Engineering Implementation

### The Agent Loop

```
func (tl *TaskLoop) Run(ctx context.Context) *LoopResult:
    for iteration := 1; iteration <= maxIterations; iteration++:
        // Record request (what the agent sees)
        recordInteraction(iteration, type=REQUEST, data=context.messages)

        // Call LLM
        response = llmClient.invoke(context.messages)

        // Record response (what the agent decided)
        recordInteraction(iteration, type=RESPONSE, data={
            content, tool_calls, finish_reason
        })

        switch response.finishReason:
        case "stop":
            return success(result=response.content)
        case "tool_calls":
            for tool_call in response.tool_calls:
                result = executeTool(tool_call)
                context.addToolResult(tool_call.id, result)
        case "length":
            // Inform agent, continue

    return failure(reason="Max iterations reached")
```

Key points:

1. Every iteration is recorded before and after the LLM call
2. The context manager maintains internal messages that never leak out
3. The final result is the only thing returned to the caller
4. New guidance can arrive via a channel at iteration boundaries, injected as environment events in the ReAct cycle

### Dependency Injection

The agent service receives its dependencies through a params struct:

```
type RunTaskParams struct {
    LLMConfig  *model.LLMConfig
    SessionID  int64
    AgentID    int64
    UserMsgID  int64
    WorkID     int64
    Guidance   string                  // Execution intent from Decide phase
    Ctx        context.Context
    OnNotify   func(data string)       // Optional SSE callback
    GuidanceCh <-chan GuidanceDirective // Channel for mid-course corrections
}
```

This distinction is important:

- **Public resources** (database session): Injected once at initialization, used throughout
- **Data objects** (LLM config, session IDs, guidance): Passed per invocation

### Cognitive Order: Comprehend → Decide → Execute

User messages may be ambiguous or context-dependent. Before execution, a cognitive order pipeline produces a clear execution intent (Guidance):

```
User message:  "Change that file"
Conversation:  [User: "Create a README.md", AI: "Created..."]

Comprehend → Decide pipeline produces:
  Guidance: "Modify README.md to update the installation instructions"

Guidance is the task requirement
```

This ensures the agent receives a clear, unambiguous task. The Guidance is injected into the system prompt as a self-directive, and new guidance can arrive during execution via the guidance channel (mid-course corrections from subsequent user messages).

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              User Layer                                      │
│                                                                              │
│   User Message ─────────────────────────────────────────────► Agent Reply   │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                      Cognitive Order Pipeline                                │
│                                                                              │
│   Comprehend (understand intent) → Decide (produce Guidance)                │
│                                                                              │
│   Guidance = execution intent, injected as self-directive in system prompt   │
│   New guidance can arrive during execution via channel (mid-course corrections)│
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Agent Service                                      │
│                                                                              │
│   ┌─────────────────────────────────────────────────────────────────────┐   │
│   │                         Agent Loop                                   │   │
│   │                                                                      │   │
│   │   Guidance ──► Context Manager ──► LLM Client                        │   │
│   │                      │                  │                             │   │
│   │                      │                  ▼                             │   │
│   │                      │         Tool Calls?                           │   │
│   │                      │              │                                │   │
│   │                      │    ┌────────┬────┬────────┐                   │   │
│   │                      │    │        │    │        │                   │   │
│   │                      │  Bash  WriteNotes  Experience  WakeMeWhen     │   │
│   │                      │    │        │    │        │    (+ WebSearch?) │   │
│   │                      │    └────────┴────┴────────┘                   │   │
│   │                      │              │                                │   │
│   │                      └──────────────┘                                │   │
│   │                             │                                       │   │
│   │                      Tool Results                                   │   │
│   │                             │                                       │   │
│   │                             ▼                                       │   │
│   │                      Next Iteration                                 │   │
│   │                                                                      │   │
│   └─────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│   ┌─────────────────────────────────────────────────────────────────────┐   │
│   │                       Interaction Recorder                           │   │
│   │                                                                      │   │
│   │   Every iteration ──► interactions table                            │   │
│   │   (invisible to user conversation)                                   │   │
│   │                                                                      │   │
│   └─────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Storage Layer                                      │
│                                                                              │
│   messages table          interactions table                                │
│   ┌─────────────────┐     ┌─────────────────────────────────────────────┐  │
│   │ user messages   │     │ session_id, user_msg_id, agent_msg_id      │  │
│   │ agent replies   │     │ iteration, type, data (JSON)               │  │
│   └─────────────────┘     └─────────────────────────────────────────────┘  │
│                                                                              │
│   (User Conversation)              (Tool Interactions)                       │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Design Principles Summary

| Principle | Implementation |
|-----------|----------------|
| **Delegation model** | Agent receives Guidance, returns delivery |
| **Interaction boundary** | Tool calls stored separately from user conversation |
| **Minimal tools** | Bash + write_notes + experience + wake_me_when cover most tasks |
| **Tool invisibility** | Unavailable tools are unknown to the agent |
| **Workspace isolation** | Each session has a confined workspace under agent_id layer |
| **Agent perspective** | Interactions record what agent saw/decided |
| **Dependency injection** | Public resources via init, data via params struct |

---

## References

- Wang, L., et al. (2025). "Toward a Theory of Agents as Tool-Use Decision-Makers." arXiv:2506.00886.
- Yao, S., et al. (2023). "ReAct: Synergizing Reasoning and Acting in Language Models." ICLR.
- Clark, A., & Chalmers, D. (1998). "The Extended Mind." Analysis, 58(1), 7-19.
- Gibson, J. (1979). "The Ecological Approach to Visual Perception." Houghton Mifflin.
