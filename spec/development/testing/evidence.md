# Isolation, Evidence, and Reports

These rules apply to deterministic tests, semantic scenarios, and experiments. Use the [test contract](README.md) to name the behavior being verified before collecting evidence; the [semantic standard](semantic.md) defines verdict authority, roles, and calibration for model-dependent cases.

## Input Provenance and Declared Sources

Credentials, fixture, configuration, and limits each have one declared source, and the run records it. Credentials come only from the operator-supplied test environment named in the case README, normally exported into the process environment from the operator's own shell profile, CI secret store, or equivalent; loading that profile for the runner is allowed. Check a secret only by presence and keep it in the process environment: never print, grep, echo, or log its value, and never pass it as a command argument. Reading credentials from product configuration, a developer data directory, or command arguments is a deviation that limits the run's provenance and must be reported.

When a required input is absent, record `ERROR`, name the missing input without revealing a secret, and ask the operator; never substitute another source. Retain that diagnostic run and treat the corrected attempt as a new run. Record the missing value as unknown in the result and explain the limitation; do not treat an empty value as a satisfied prerequisite.

## Evidence Lifecycle and Purity

When a case separates capture from judgment, capture writes mechanical facts once and later stages append to them without overwriting; [semantic.md](semantic.md#result-record) defines how a semantic result validator enforces that split. The final judge owns the final artifact manifest, and evidence added after capture must update the manifest in the same result write. The evidence set contains only auditable artifacts; binaries, build products, credentials, and developer state are not evidence.

## Result Meanings

| Result | Meaning |
| --- | --- |
| `PASS` | The required checks were completed and the recorded evidence supports every required criterion. |
| `FAIL` | An observed result contradicts a predeclared criterion. |
| `INCONCLUSIVE` | The run completed but the evidence or criteria do not support a reliable pass or fail judgment. |
| `ERROR` | Setup, tooling, credentials, or execution failed before the scenario could be evaluated. |

An `ERROR` is not a product defect, and neither `ERROR` nor `INCONCLUSIVE` is a pass. For ordinary Go tests, also retain the test command's exit status and failure output; this table does not replace the runner's result. The [semantic standard](semantic.md#status-by-cause-and-machine-interface) defines the missing-record classification; each S case documents its executable exit codes in its README or shared case documentation.

## Result and Report Layout

For an executable case, `result.json` is the machine-readable status and `report.md` explains its evidence. Validate their common layout and status; when the case has a separate definition or result contract, run its validator too. The report contains exactly one top-level `Status: <PASS|FAIL|INCONCLUSIVE|ERROR>` line, and its value must equal `result.json.status`; result writing and final validation must reject a missing, duplicate, or conflicting report status. A semantic case's final validation checks the completed judgment. Capture-only validation checks a scaffold and cannot report a completed test result or return a passing gate. An Agent-only case follows its documented validation path. A case runner or final-result validator returns zero only for a validated final `PASS`; definition validators have their own success meaning. `FAIL`, `ERROR`, and `INCONCLUSIVE` remain distinct in the report. PR decisions follow the separate [review standard](../../collaboration/review.md).

For every case run, use the [report directory layout](case_directory.md#file-roles). Keep `result.json` and `report.md` at the run root and place all inputs, product data, logs, and supporting evidence under `data/`. The result records at least `case_name`, `run_id`, track, status, a short detail, execution command, start and end times, artifact paths, and limitations; the Markdown report states the observed conclusion, how the case was run, which checks or criteria passed or failed, where the supporting evidence is, and what remains unverified. Case-specific result fields and validation rules may add detail. If the case definition itself cannot load, write a minimal `ERROR` result and report with the known fields; that record is diagnostic and cannot be a validated final `PASS`.

For TypeScript case runners, use `RunRecord` from the shared [`tools.ts`](../../../tests/cases/tools.ts) to create and finalize this layout. Supply the track-specific validator when writing the result; the common utility does not decide whether a product check or semantic criterion passes.

A result validator must reject absolute or escaping artifact paths, missing referenced files, and an input checksum mismatch where one is recorded. For an S case, it must also reject `PASS` with missing criterion evidence, an unsupported judgment, or a missing judge rationale, and must keep mechanical failures fixed. A recorded calibration that shows unreliable judgment cannot support `PASS`; calibration is optional when a run can be judged directly from its evidence. Build from current local source; do not require Git metadata or a clean worktree for a test result. Store model judgment such as `supported`, `contradicted`, or `insufficient_evidence` separately from the run status.

## Isolation and Artifacts

- Run each stateful case in a fresh, uniquely named directory under its own `<case>/reports/`. Keep its data root, database, working files, build cache, logs, and port separate from other cases and runs. Preserve real user data and services. Track the processes started by the test and clean up only those processes and owned resources; retain evidence needed for diagnosis.
- Use the real business entry point where the scenario claims end-to-end behavior. Read-only database queries or logs may establish resulting state. Direct fixture writes must be identified as setup and cannot stand in for a product action. Test-only observability must not become a production backdoor.
- Keep API keys and private user content out of fixtures, Git, and shared reports. Inject credentials through the execution environment. Treat model inputs, outputs, traces, logs, and temporary databases as potentially sensitive artifacts, with an explicit storage and retention scope.
- Record enough to audit a result: case directory name, definition and fixture checksums when applicable, environment, model/provider configuration when applicable, inputs, commands or actions, actual outputs, evidence locations, assertions, verdict rationale, and limitations. Verify that cited artifacts exist and belong to the run. Record uncontrollable model variation rather than promising exact replay.
- Before an S/X run, declare total or phase-specific time limits and a retry limit, including zero when the executor does not retry; declare a model-call limit when the case invokes a model. Enforce limits at the provider or product boundary when possible; record observed calls and any unenforceable billing exposure. Each case's local data is isolated, but a shared external provider account can still impose common quotas or costs; record that dependency and avoid claiming provider isolation. Use a provider-side budget or cap when available to control spending; it does not determine whether the observed behavior passes the test. If a limit is exceeded, stop the owned run and use `INCONCLUSIVE` or `ERROR` according to whether enough evidence was collected.
- State which checks ran, which failed or could not run, and which case criteria remain unverified. A test agent's report contains the run verdict and does not approve or merge a PR.
- Keep generated reports under the case's Git-ignored `reports/` directory and do not commit them. Keep local artifacts long enough to investigate a result, and do not commit raw private data or secrets. A standard Go test normally needs no separate report beyond its command output.
