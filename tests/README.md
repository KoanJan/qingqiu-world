# Cross-Component Tests

Start with the [case index](cases/README.md). Cross-component cases are grouped by track: `D/` for deterministic checks, `S/` for semantic judgments, and `X/` for mechanism experiments. Each case has its own README and the inputs, runner, validators, definition, or Agent instructions it needs. Open a case README to see its prerequisites, run command, and expected result. Runs write to that case's Git-ignored `reports/<run>/` directory.

| Case | What it checks |
| --- | --- |
| [background-upload-restart](cases/D/background-upload-restart/README.md) | Background upload, byte-identical readback across restart, invalid-extension rejection, and deletion. |
| [user-profile-restart](cases/D/user-profile-restart/README.md) | User profile persistence and immutable name across server restart. |
| [rag-source-order](cases/S/rag-source-order/README.md) | Answer from an ordered source. |
| [rag-insufficient-evidence](cases/S/rag-insufficient-evidence/README.md) | Acknowledge missing source evidence. |
| [schedule-reminder-from-chat](cases/S/schedule-reminder-from-chat/README.md) | Create a scheduled reminder matching a chat request's time and content. |
| [relay-message-to-person](cases/S/relay-message-to-person/README.md) | Deliver a user's relay to the named Person and preserve its key information. |

Use Node.js 20.19+ (20.x) or 22.12+. Install dependencies with `npm --prefix tests ci`, then check the TypeScript code with `npm --prefix tests run typecheck` and the offline contracts with `npm --prefix tests run test:unit`. To run a case, follow its README from the repository root. To run every D case, use [`npm --prefix tests run test:d`](cases/D/README.md). To add a case, follow [Add a case](cases/README.md#add-a-case) and the [directory standard](../spec/development/testing/case_directory.md), then update this index. The [test engineering standard](../spec/development/testing/README.md) defines verification and evidence requirements.

Ordinary Go package tests remain next to their code, including the [KB creation regression](../server/internal/service/kb/kb_service_create_test.go) and [document route scope regression](../server/internal/api/handler/kb_document_scope_test.go). The [CI exit-status test](../ci/test_ci_exit_status.sh) stays beside the CI script it checks. Web tests should likewise live with the Web code when added. The current `tests/cases/` entries include two deterministic end-to-end cases and four semantic cases; no mechanism experiment is implemented there yet.

## Server data during a test run

The shared `IsolatedServer` builds current workspace code with ordinary `go build` and selects `TEST_DATA_MODE=1`. A test run may use uncommitted code. Startup prepares the released schema through the normal release initializer or migration, then applies the registered unreleased steps before starting business services. It does not enter the developer data lifecycle or require a clean checkout, Git tags, or a stamped development binary.

The runner fixes `DATA_ROOT` at `data/product/` inside the run and passes `TEST_MIGRATION_STATE_PATH` as the absolute path to `data/test-migrations.json`. This progress file belongs to the test infrastructure; no test metadata is added to product files or database tables. `db_versions` continues to record released versions only. Developer and release-only environment switches are overridden so inherited shell settings cannot select another startup path.

A normal restart reuses the same binary, product data, and progress file, skipping completed unreleased steps. New runs and rebuilt code use newly prepared data. Any preparation error ends the run and preserves its artifacts for inspection; rerun with new data rather than resuming a failed preparation. There is no test-specific rollback or recovery mechanism.

This path verifies current code against released data plus current unreleased changes. It does not verify the developer lifecycle or replace acceptance of the final release upgrade.
