# Deterministic Tests and Checks

Apply the [test contract and risk selection](README.md) before implementing a case. Record its result under [evidence and reports](evidence.md).

For a case under `tests/cases/D/`, follow the [case directory layout](case_directory.md). Ordinary package and component tests stay beside their code. Run a D case through its own README when checking a specific behavior; run all D cases from the repository root with `python3 tests/cases/D/runall.py`. The [aggregate runner](../../../tests/cases/D/README.md) returns nonzero when any case fails or lacks `scripts/run.py`; each case writes its own run result under `<case>/reports/`.

- Exercise domain rules, state transitions, permission boundaries, causal links, and failure behavior with executable assertions. Test observable behavior and invariants rather than incidental call counts or internal structure, unless those details are themselves the contract.
- Use real temporary SQLite files for persistence, transaction, locking, and migration behavior. Verify upgrades from representative data at the latest released version and inspect historical associations; a fresh database alone does not prove upgrade safety. Follow [local data snapshot and restore](../../collaboration/local_data.md) when testing an active Issue against a developer's release baseline.
- Test time, concurrency, cancellation, retries, and restart recovery at their observable boundaries. A fake clock or controlled failure may be used in test code when it makes the behavior reproducible. Fake model and embedding responses may verify orchestration and error handling; they do not demonstrate real model semantics or external API compatibility.
- Keep test substitutes and failure controls in tests or test-only builds. Do not expose a production endpoint that can alter state solely for a test. Reuse existing test utilities when they express the same boundary; avoid a generic runner or shared abstraction before it solves repeated work.
- Run the relevant tests, static checks, and builds for the affected code. A check designated as required must return failure when any of its required tests fail. Do not mask a failure as "pre-existing" inside a passing script. Diagnose it, repair it, or seek an explicit change to the required-check policy through project review; record the verification gap. Report skipped or unrun checks separately from passing checks.

## Implementing a Go or API test in this repository

- Put a Go `*_test.go` file beside the package it tests. Start with a named `Test...` case for one observable contract. Use table cases for meaningful input or state variations, not to duplicate the implementation's branches mechanically.
- For pure rules, call the real function with controlled inputs. For API behavior, call the real Gin handler or router through `httptest` and assert the response **and** resulting state. For an external provider contract, use a local HTTP server to test request shape and error handling, then run a separate, explicitly gated live contract check when provider behavior matters.
- For database behavior, create a unique file under `t.TempDir()`. Use the real SQLite driver and the tables required by the test. Set `DATA_ROOT` and `LOG_DIR` to that directory when exercising application initialization; use the actual database initialization path when WAL, indexes, or startup behavior are part of the contract. A direct GORM connection with selected models is suitable for a narrower domain test. Do not initialize today's schema before a historical migration test: start from a preserved released-version fixture, run the actual upgrade path, and check records and repeated-run behavior.
- Some existing Go tests replace package-level `database.DB` or initialize the configuration singleton. Save and restore `database.DB` with `t.Cleanup`, close owned database connections, and do not call `t.Parallel` in a test that mutates shared globals. `config.Init` has no public reset operation: a test that changes its singleton must stay in a controlled package or process and must not assume `t.Setenv` restores the singleton. Use channels, barriers, controlled clocks, or bounded polling around observable conditions; arbitrary sleeps must not decide correctness.
- Verify a transaction by observing committed records after success and their absence after rollback. Verify permissions through each relevant read path, including direct ID access. Verify recovery by repeating it and checking for duplicate or lost effects. If the claim includes process restart, start and stop an owned server process rather than only invoking its recovery helper.

Here is a complete deterministic case already implemented in `server/internal/service/runtime/event_buffer_shutdown_test.go` as `TestInterruptedEventBufferPreservesUndecidedDelivery`:

| Contract item | Implementation in the case |
| --- | --- |
| Risk and subject | An interrupted, undecided event might be lost or buffered twice; test `agentRuntime.bufferInterruptedEvent` and its persisted buffer. |
| Setup | Create `shutdown.db` under `t.TempDir()`, migrate `Decision` and `AgentEventBuffer`, replace `database.DB` for the test, and register cleanup to restore it. Construct an event with a fixed person, session, event ID, and message payload. |
| Action | Buffer the event twice; read the buffer; insert a persisted `Decision` accepting that event; attempt to buffer it again. |
| Oracle | Exactly one buffered row exists; deserialization preserves event, session, message ID, and content; the final buffer attempt does not add a row. |
| Execution | From `server/`, run the focused command below, followed by the runtime package suite. Keep the command and result in the PR verification record. |
| Claim limit | This case proves buffering and replay data integrity at the package boundary. It does not start a new process, so it does not claim full restart recovery. |

For a new case, substitute its own risk, action, and assertions in this same structure. Do not copy this case under another name when it already detects the risk.

Run a focused Go case from `server/`, then widen only as the change requires:

~~~bash
go test ./internal/service/runtime -run '^TestInterruptedEventBufferPreservesUndecidedDelivery$' -count=1
go test ./internal/service/runtime -count=1
go test ./... -count=1 -short
go vet ./...
go build ./...
~~~

Use `go test -race` on affected concurrent packages when the risk is a data race; a passing race run does not establish all ordering invariants. The root `ci/ci.sh` must propagate a required Go test failure through its exit status; keep that behavior covered by its isolated exit-status test.

When changing `ci/ci.sh`, run `ci/test_ci_exit_status.sh` as a focused contract test and include its result in the PR verification record. The root `ci/ci.sh` also runs that contract; its disposable child invocations skip the contract step to avoid recursion. This does not claim an existing hosted CI gate.

## Implementing a Web, Electron, or cross-process test

- The current Web project has build and lint commands but no test framework. For a deterministic UI state or interaction that will recur, add a focused test using a suitable framework only when it provides a reliable assertion. For a small visual change, use the checks in the [Web standard](../web_project.md) and record the actual viewport, language, data conditions, and observation.
- Use the actual API and desktop boundaries for a claim about configuration persistence, packaging, or process lifecycle. A component test cannot establish that the packaged Electron application starts, and a server build cannot establish that its persisted data survives an upgrade.
- For a cross-process case, assign a unique `DATA_ROOT`, `LOG_DIR`, and `PORT`; track the process started by the test and terminate that process only. Wait for readiness before acting. Preserve logs and the isolated data root on failure, and never point a test at a developer's live data directory.
- [user-profile-restart](../../../tests/cases/D/user-profile-restart/README.md) exercises this boundary through the real user-profile API and two owned server processes. It checks immutable user identity and updated bio after restart, then reads the isolated SQLite database after shutdown. Use it as an executable example of a deterministic cross-process case; keep its assertion in the runner rather than adding a separate scenario manifest for fixed input and output.
- Run the existing root scripts from the repository root as applicable: `npm run build:web`, `npm run build:electron`, and `npm run build:server`. Record platform-specific checks that could not run.
