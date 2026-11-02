# Go Project Standards

These requirements apply to `server/` alongside the other development standards in this directory.

## Organization and Types

- Organize packages by domain responsibility. Each fact or behavior should have a clear owner; multiple callers should share one implementation rather than copying a business rule across layers.
- Keep dependencies aligned with the existing architecture. Extract a package to resolve actual mixed responsibilities or a dependency cycle, not to add an abstraction layer or domain entity for one call.
- Use a dedicated `int`-based type and constants for domain enums, never string enums. When adding or changing a persisted enum, review migrations, serialization, and frontend mappings together.
- Name domain thresholds and fixed values as constants in the relevant area. Put a value in configuration only when deployments need to adjust it. Do not maintain the same default separately in several files.

## Behavior and Errors

- Function boundaries should make input constraints, state changes, and failure behavior apparent. Handle valid absence, an event that has not yet happened, and anomalous data separately; do not collapse all errors into an empty result.
- Include a relevant record ID, operation, and cause in diagnostic logs, while avoiding secrets, large amounts of private content, or complete model context.
- Use a transaction when multiple records jointly express one fact. For asynchronous work, account for start, completion, cancellation, and state after restart. Do not rely on process-local state for a business result that must persist.

## Comments and Verification

- Go comments should explain packages, exported types, functions, important branches, and easily misunderstood domain boundaries. Do not restate obvious steps in comments; preserve the non-obvious reasons.
- Focus tests on permissions, state transitions, causal links, upgrade paths, and known regression risks. Automate deterministic rules where practical. Evaluate model-dependent semantics using reproducible inputs and records.
- After a change, run the relevant Go tests and build. State which checks were not run, the environment used, and any material unverified paths.
