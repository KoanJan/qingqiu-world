# Database Standards

Schema design should serve persistent data and its query paths. Do not add a table merely because a page or prompt temporarily needs a particular layout.

## Data Modeling

- Every column is `NOT NULL`. Express an absent value using an explicit domain default or a distinct state, and explain its meaning in code. Do not let an unqualified zero value conflate "unknown," "not yet happened," and "no association."
- Do not use foreign keys or other cross-table database constraints. Persist associations by ID and check their validity in the application when creating, updating, deleting, or reading them. Use a transaction when one business operation must take effect atomically.
- Persist domain enums as integers and declare dedicated types for them in Go. When a persisted enum changes, inspect historical data, caches, migrations, and query predicates; changing only the constant order is insufficient.

## Provenance, Deletion, and Derived Data

- Define ownership, lifetime, and access permissions for each kind of data. Before deleting or deactivating data, inspect references, derived indexes, and read/write paths. Decide how history is handled; do not leave readable records whose origin can no longer be explained.
- Rebuildable indexes and summaries are derived data. Failures, missing sources, and incomplete coverage must be observable. Do not report that all sources are indexed merely because one scan completed.
- Each query entry point should define the owner, observable scope, and permission filtering. Pagination, time ranges, and ordering need deterministic semantics. Distinguish an empty result from query failure, insufficient permission, or incomplete index coverage.

## Version Upgrades

- Organize data migrations by release version. Multiple Issues for the same upcoming release continually refine one version migration function. Preserve already merged steps and review their order and data semantics. Do not rewrite a release's migration after that release is published.
- Treat schema changes and one-time data migrations separately. A data migration must define its source version, idempotency, retry behavior after failure, and mapping from old values to new ones. Do not leave a one-time backfill in the routine startup path.
- Verify a real upgrade path and inspect historical data for dangling references. Passing tests against only an empty database does not establish upgrade safety.
- Do not silently discard historical data that still has business meaning. If an obsolete field or type must be removed, explain the scope and visible impact in the release design.
