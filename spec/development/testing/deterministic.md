# Deterministic Tests and Checks

Apply the [test contract and risk selection](README.md) before implementing a case. Record its result under [evidence and reports](evidence.md).

For a case under `tests/cases/D/`, follow the [case directory layout](case_directory.md). Ordinary package and component tests stay beside their code. Run a D case through its own README when checking a specific behavior; run all D cases from the repository root with `npm --prefix tests run test:d`. The [aggregate runner](../../../tests/cases/D/README.md) returns nonzero when any case fails or lacks `scripts/run.ts`; each case writes its own run result under `<case>/reports/`.

- Exercise domain rules, state transitions, permission boundaries, causal links, and failure behavior with executable assertions. Test observable behavior and invariants rather than incidental call counts or internal structure, unless those details are themselves the contract.
- Use real temporary SQLite files for persistence, transaction, locking, and migration behavior. Verify upgrades from representative data at the latest released version and inspect historical associations; a fresh database alone does not prove upgrade safety. Follow [local data snapshot and restore](../../collaboration/local_data.md) when testing an active Issue against a developer's release baseline.
- Test time, concurrency, cancellation, retries, and restart recovery at their observable boundaries. A fake clock or controlled failure may be used in test code when it makes the behavior reproducible. Fake model and embedding responses may verify orchestration and error handling; they do not demonstrate real model semantics or external API compatibility.
- Keep test substitutes and failure controls in tests or test-only builds. Do not expose a production endpoint that can alter state solely for a test. Reuse existing test utilities when they express the same boundary; avoid a generic runner or shared abstraction before it solves repeated work.
- Run the relevant tests, static checks, and builds for the affected code. A check designated as required must return failure when any of its required tests fail. Do not mask a failure as "pre-existing" inside a passing script. Diagnose it, repair it, or seek an explicit change to the required-check policy through project review; record the verification gap. Report skipped or unrun checks separately from passing checks.

## One Hypothetical D Case

Suppose a fictional record API lets account A create a private note. Only A may read it, and the note must survive a server restart. This is an illustration, not a feature or case in this repository.

| Contract item | Hypothetical case |
| --- | --- |
| Risk and subject | A note may disappear on restart, become readable by account B, or be stored twice. Exercise the public API and persisted state. |
| Setup | Start an owned server with a fresh `DATA_ROOT`, `LOG_DIR`, port, and SQLite database. Create A and B through supported product setup, without writing the note directly to the database. |
| Action | A creates one note with fixed content. A and B each request it. Stop the owned server, restart against the same isolated data root, and repeat both reads. |
| Oracle | A receives the same note ID and exact content before and after restart. B receives HTTP 403, the denial response declared by this fictional API, both times. A read-only database check after shutdown finds exactly one row with that ID and content. An observed contradiction is `FAIL`; setup or tooling failure is reported separately. |
| Execution and evidence | `scripts/run.ts` makes the requests, checks responses and final state, and writes requests, responses, database observation, and both process logs under this run's `data/`. It produces `result.json` and `report.md` through the shared report utility. |
| Claim limit | A passing run supports this API, authorization, and restart-persistence contract. It does not establish browser behavior or migration from an older database version. |

Its committed files would be `README.md` and `scripts/run.ts` under `tests/cases/D/private-note-restart/`; no duplicate scenario file is needed for these fixed inputs and assertions. Execution creates a Git-ignored `reports/<run>/` containing `result.json`, `report.md`, and `data/`. The README would state the exact command, prerequisites, data, expected response, evidence locations, and result meanings. The runner would return success only after all fixed assertions pass and the final report status matches the result.

## Implementing a Go or API test in this repository

- Put a Go `*_test.go` file beside the package it tests. Start with a named `Test...` case for one observable contract. Use table cases for meaningful input or state variations, not to duplicate the implementation's branches mechanically.
- For pure rules, call the real function with controlled inputs. For API behavior, call the real Gin handler or router through `httptest` and assert the response **and** resulting state. For an external provider contract, use a local HTTP server to test request shape and error handling, then run a separate, explicitly gated live contract check when provider behavior matters.
- For database behavior, create a unique file under `t.TempDir()`. Use the real SQLite driver and the tables required by the test. Set `DATA_ROOT` and `LOG_DIR` to that directory when exercising application initialization; use the actual database initialization path when WAL, indexes, or startup behavior are part of the contract. A direct GORM connection with selected models is suitable for a narrower domain test. Do not initialize today's schema before a historical migration test: start from a preserved released-version fixture, run the actual upgrade path, and check records and repeated-run behavior.
- When a test replaces package-level `database.DB`, restore it with `t.Cleanup` and close owned connections. Do not call `t.Parallel` in a test that mutates shared globals. `config.Init` has no public reset operation: keep a test that changes its singleton in a controlled package or process and do not assume `t.Setenv` restores it. Use channels, barriers, controlled clocks, or bounded polling around observable conditions; arbitrary sleeps must not decide correctness.
- Verify a transaction by observing committed records after success and their absence after rollback. Verify permissions through each relevant read path, including direct ID access. Verify recovery by repeating it and checking for duplicate or lost effects. If the claim includes process restart, start and stop an owned server process rather than only invoking its recovery helper.

For a new Go test, state its risk, setup, action, exact assertions, execution boundary, and claim limit using the [test contract](README.md#implement-one-test). Check existing coverage before adding another test of the same risk. Run the focused test from `server/`, then the affected package and wider checks as the change requires:

~~~bash
go test ./path/to/package -run '^TestName$' -count=1   # focused case
go test ./path/to/package -count=1                      # affected package
go test ./... -count=1 -short
go vet ./...
go build ./...
~~~

Use `go test -race` on affected concurrent packages when the risk is a data race; a passing race run does not establish all ordering invariants. The root `ci/ci.sh` must propagate a required Go test failure through its exit status; keep that behavior covered by its isolated exit-status test.

When changing `ci/ci.sh`, run `ci/test_ci_exit_status.sh` as a focused contract test and include its result in the PR verification record. The root `ci/ci.sh` also runs that contract; its disposable child invocations skip the contract step to avoid recursion. This test does not establish that `ci/ci.sh` runs in hosted CI.

## Implementing a Web, Electron, or cross-process test

- For a deterministic UI state or interaction that will recur, add a focused test using a suitable framework when it provides a reliable assertion. For a small visual change, use the checks in the [Web standard](../web_project.md) and record the actual viewport, language, data conditions, and observation.
- Use the actual API and desktop boundaries for a claim about configuration persistence, packaging, or process lifecycle. A component test cannot establish that the packaged Electron application starts, and a server build cannot establish that its persisted data survives an upgrade.
- For a cross-process case, assign a unique `DATA_ROOT`, `LOG_DIR`, and `PORT`; track the process started by the test and terminate that process only. Wait for readiness before acting. Preserve logs and the isolated data root on failure, and never point a test at a developer's live data directory.
- Keep a fixed cross-process assertion in the D runner rather than adding a separate scenario manifest for the same input and output. The case README states the exact entry point and commands; the runner must retain the observed API result and final state as evidence.
- Run the root build scripts from the repository root as applicable: `npm run build:web`, `npm run build:electron`, and `npm run build:server`. Record platform-specific checks that could not run.
