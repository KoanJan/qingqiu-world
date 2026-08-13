// Package comprehend is the comprehension phase of the agent pipeline.
//
// It is a router plus a public API facade: the parent package exposes the
// entry point (Comprehend), the shared result types, and a few cross-cutting
// signals that the execution phase needs. Event-type-specific comprehension
// logic lives in sub-packages (for example, chat handles private chat message
// events).
//
// External callers should import only this package; they must not reach into
// the sub-packages directly.
package comprehend
