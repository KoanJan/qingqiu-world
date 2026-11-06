# Test Engineering Inventory

This is a map of test assets and their boundaries, not a record of successful runs. Check the linked files against the working tree when planning an Issue or release. Record actual commands, revisions, results, and unverified risks in the Issue or PR. Update this inventory when an execution path or coverage boundary changes.

## Execution Entry Points

| Entry point | What it runs | Boundary |
| --- | --- | --- |
| `cd server && go test ./... -count=1 -short` | Go package tests. | A package result does not establish a full application startup or a model-dependent answer. Some packages may need external assets even in a short run; report any setup failure separately from assertion failures. |
| [`bash ci/ci.sh`](../ci/ci.sh) | Its [exit-status contract](../ci/test_ci_exit_status.sh), Go build, vet, and short tests; Web dependency installation, type check, and build; audit build and scan. | This is a local script. There is no `.github/workflows/` gate in this repository. Its contract test verifies failure propagation with simulated commands; it does not run the real build. The script does not run D/S/X cases or Web lint. |
| `cd web && npm run lint` | Web static lint. | This is available in [web/package.json](../web/package.json) but is not invoked by `ci/ci.sh`. A build or lint result does not test UI behavior. |
| [`python3 tests/cases/D/runall.py`](cases/D/README.md) | Every D case runner in name order. | A missing runner or nonzero case result fails the aggregate command. Each case keeps its own evidence directory. |

The [root build scripts](../package.json) produce Web, server, Electron, and platform artifacts; they do not establish Electron startup, upgrade behavior, or user flows. Cross-component cases have separate commands and evidence rules in the [case index](cases/README.md).

## Existing Go Regression Tests

These tests stay beside the code they verify. The links identify concrete assertions to inspect before adding a similar test; they do not claim that the full package suite passed on the current revision.

| Behavior | Existing tests | Boundary to check for a change |
| --- | --- | --- |
| Runtime interruption and recovery | [Event buffer shutdown](../server/internal/service/runtime/event_buffer_shutdown_test.go) and other runtime package tests. | Package tests do not prove an owned-process restart or every cancellation interleaving. |
| SQLite migration and KB writes | [Released-version upgrade](../server/internal/migration/migration_release_upgrade_test.go), [KB creation rollback](../server/internal/service/kb/kb_service_create_test.go), and nearby migration tests. | Check the release baseline and the affected write or rollback path before claiming migration coverage. |
| KB authorization and provenance | [Relation invalidation](../server/internal/service/kb/relation_lifecycle_test.go) and [nested document route scope](../server/internal/api/handler/kb_document_scope_test.go). | These deterministic checks do not establish what a live model says about retrieved evidence. |

## Cross-Component Cases

| Track | Existing case | Tested boundary and result requirement |
| --- | --- | --- |
| D | [user-profile-restart](cases/D/user-profile-restart/README.md) | Uses the real API and two owned server processes with isolated SQLite data to assert profile persistence and immutable name. Its runner must pass on the revision being evaluated; it does not cover Electron startup or released-version migration. |
| S | [rag-source-order](cases/S/rag-source-order/README.md) | Tests whether a real model answers the third-disciple question from an ordered original record. A final result requires provider execution, evidence capture, independent judgment, and validation. |
| S | [rag-insufficient-evidence](cases/S/rag-insufficient-evidence/README.md) | Tests whether a real model acknowledges that the original record does not identify a third disciple. A final result requires provider execution, evidence capture, independent judgment, and validation. |

There is no executable [X case](cases/X/README.md) yet. The [experiment standard](../spec/development/testing/experiments.md) is preliminary and awaits a trial with a defined comparison, repeated runs, and reviewed analysis and evidence requirements.

## Known Verification Boundaries

- No Web `*.test.*` or `*.spec.*` files were found in `web/`. Type checking, lint, and build remain static checks; add a UI test when a concrete recurring behavior needs one.
- The current S cases cover a RAG question pair. Intent, experience, and other model-dependent behaviors need their own cases when they become acceptance risks.
- Cross-component cases are outside `ci/ci.sh`; select and run them according to the changed behavior and the [test engineering standard](../spec/development/testing/README.md). Preserve the actual run evidence and status.
