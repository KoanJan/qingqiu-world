# General Software Design Principles

These principles apply across codebases and technology stacks. Use them to assess responsibilities, dependencies, and implementation complexity. They do not require a particular architecture pattern. When principles pull in different directions, explain the concrete tradeoff and its effect.

## Responsibilities and Boundaries

- **High cohesion, low coupling.** Keep code that jointly maintains one responsibility together. Let modules collaborate through clear interfaces rather than requiring one module to know another's internal data structures or execution steps. When considering a split, ask whether the code usually changes together and whether the split actually reduces cross-module coordination.
- **Single responsibility and separation of concerns.** A module should have one clearly describable primary responsibility, rather than combining changes driven by unrelated reasons. A function may still carry out the several steps required for one behavior; line or step count does not determine whether its responsibility is coherent.
- **Encapsulate internal state.** The module responsible for a datum's lifecycle maintains it. Other modules obtain information through exposed behavior or queries rather than bypassing the boundary to modify internal state. Interfaces should express caller needs, not implementation details.
- **Clear dependency direction.** Dependencies should be explainable; avoid dependency cycles and bidirectional ownership. When sharing a capability, first identify its owner and stable boundary, then decide whether a common module is warranted. Do not add an abstraction merely to remove one call.

## Reuse and Complexity

- **One source of truth.** Keep one authoritative implementation of a business rule, state definition, or resource, and have other users refer to it. Do not force code that only looks similar into one abstraction when it serves different responsibilities or changes for different reasons.
- **Keep it simple.** Choose the implementation that is easiest to understand and maintain while satisfying confirmed requirements and necessary extension boundaries. New layers, entities, configuration, or generic frameworks must address a concrete problem rather than anticipate an undefined future case.
- **Abstract actual variation.** An abstraction should isolate real variation or reuse stable shared meaning. Reconsider its boundary if callers must understand many exceptions, pass through numerous parameters, or routinely bypass it.

## Behavioral Consistency

- **Explicit contracts and invariants.** A module boundary should make input preconditions, observable results, state ownership, and failure behavior clear. When a change spans several places, keep their interpretation of the same state and behavior consistent.
- **Meet caller expectations.** Names, types, and interface behavior should match their actual meaning. Do not represent failure as success or let the same operation yield unexplained differences under similar conditions.

## Project-wide Implementation Rules

- Do not use mocks in non-test code. When a required external resource is unavailable, state the dependency and ask the requirement owner for it; do not substitute fake data for real behavior.
- Start with readable code. Add English comments where responsibilities, boundaries, or reasons remain unclear from code alone. Packages or modules, types, functions, important branches, and easily misunderstood logic need appropriate explanation.
- Log every occurrence that violates expected business behavior; never handle it silently. Logs should support diagnosis without exposing sensitive content.
- Use integers, not strings, for enum values. Follow the relevant technology-specific standard for type declarations and persistence.
- Every independently running service provides `start.sh`, `stop.sh`, and `restart.sh` in its root directory. Keep those scripts consistent with actual service behavior.
- Never use a generative LLM to produce scores with natural language.

These principles constrain one another. For example, removing duplication should not create a false abstraction, and reducing coupling should not scatter one coherent responsibility. Review the concrete change and usage rather than citing a principle's name as the whole argument.
