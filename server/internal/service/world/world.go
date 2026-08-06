// Package world describes the world in which Persons live.
//
// The world is the constant stage on which agents act. It owns the rules of
// reality — how Energy behaves, what kinds of action are physically possible
// — and exposes them as a single immutable description. The world itself is
// not bound to any particular agent; agents come and go, but the world rules
// stay fixed for as long as the world is running.
//
// The runtime rules themselves (Energy recovery/deduction, sleep on
// exhaustion, event buffering and replay) are implemented elsewhere — by
// the energy, runtime, and eventqueue packages. What this package owns is
// only the descriptive text: a stable string that the Decide phase uses as
// a prompt prefix so the LLM understands the world it acts in.
//
// The description intentionally does NOT contain any dynamic state — no
// current Energy, no current time, no list of sessions. Those are appended
// separately by the Decide phase. Keeping the description static preserves
// LLM prefix caching and makes the rules a single source of truth.
package world

// WorldDescriptions is the single, immutable source of truth describing the
// rules of the current world.
//
// It is a static string — it does not read the database, AgentState, sessions
// or events. It describes:
//   - The world is shared with other named Persons and events happen
//     regardless of any single agent's ability to perceive them.
//   - Energy is limited, recovers with time, and exhaustion makes an agent
//     unable to perceive, decide or act; events that occur during that
//     inability remain in the world and may be encountered later.
//   - The agent may use an available ability to begin a conversation with
//     another Person. A conversation it begins becomes an event in the
//     world, and the other Person may encounter it according to their own
//     capacity and circumstances.
//   - The agent may use an available ability to set an alarm that will wake
//     it at a future time. Setting an alarm is one of its actions and may
//     consume Energy.
//   - Actions may succeed, fail, be refused by the environment or be
//     interrupted; their results become observable events.
//
// The text describes facts of the world, not strategies or value judgments.
// It does not say who the agent should contact, when it should speak, or
// whether helping is good. Those are the agent's own decisions.
//
// The description is treated as a prompt prefix by the Decide phase. Append
// dynamic state (current Energy, current time, etc.) after it.
const WorldDescriptions = `You live in a world shared with other named Persons. Events happen whether or
not you can perceive them.

Your Energy is limited and recovers with time. When you have no Energy, you
cannot perceive events, form decisions, or act. Events that occur while you
cannot act remain in the world and may be encountered when your capacity
returns.

You may use an available ability to begin a conversation with another Person.
A conversation you begin becomes an event in the world. The other Person may
encounter it according to their own capacity and circumstances.

You may use an available ability to set an alarm that will wake you at a
future time. When the alarm fires, you will receive the context you provided.
Setting an alarm is one of your actions and may consume Energy.

Your actions may succeed, fail, be refused by the environment, or be
interrupted. Their results become observable events.`
