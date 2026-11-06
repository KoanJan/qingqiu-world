# Test Engineering

These standards govern verification during development, PR review, and release preparation. They complement the [development standards](../../README.md) and [PR review requirements](../../collaboration/review.md). Release designs and Issues define the behavior to verify; these documents define how to obtain trustworthy results. They do not require a new test framework, service, or test for every change.

Before claiming a new test track is ready, inventory the existing tests, build commands, external dependencies, and isolation boundaries. Map proposed high-risk cases to existing coverage and record gaps instead of treating a target case count as proof. Keep the repository-specific inventory with its test assets; this repository currently uses `tests/engineering-inventory.md`.

## Choose the Test Path

| Need | Standard |
| --- | --- |
| Directory layout for a cross-component case | [Case directories](case_directory.md) |
| Domain rule, persistence, API, UI, restart, or build check | [Deterministic tests](deterministic.md) |
| Model-dependent answer or agent behavior | [Semantic scenarios](semantic.md) |
| Effect of a mechanism over repeated runs | [Experiments](experiments.md) (X; preliminary, to be completed through a future case) |
| Isolation, artifacts, or result reporting | [Evidence and reports](evidence.md) |

Each [case directory](case_directory.md) owns the files needed for its execution and judgment, plus an operator README. The directory name is the case identity; its D/S/X parent identifies the track. When a case uses a scenario definition, that definition owns its input, criteria, and limits, without a duplicate case ID. The README explains prerequisites, commands, data, expected and forbidden behavior, evidence, and result interpretation. An executor may carry out repeatable product setup and evidence capture, but it must load fields defined by its scenario rather than maintain a second test definition. The current D track has an [aggregate runner](../../../tests/cases/D/README.md); S cases follow their individual guides. Future X cases will require their own guides and a completed experiment method.

## Implement One Test

Before writing code or a scenario file, complete this small contract. The Issue or release design supplies the expected behavior; the test author turns it into an observable check.

| Decision | Record before execution |
| --- | --- |
| Risk | What concrete failure would matter, and which existing test, if any, already detects it? |
| Subject | Which public behavior, package function, HTTP route, UI flow, or startup path is under test? |
| Setup | Which versioned fixture, initial records, permissions, clock, and external dependencies are required? |
| Action | Which operation or sequence creates the risk, including the failure or concurrency boundary? |
| Oracle | What exact persisted state, response, trace, source text, or behavior distinguishes pass from fail? Which results are in scope, how many are allowed, and what must **not** happen? |
| Execution | Where will the test live, how is it run alone, and when must it run again? |
| Evidence | What output must be kept so another contributor can verify the result? |

Then implement in this order:

1. Find the existing implementation, nearby tests, and applicable Issue acceptance criteria. Confirm that the proposed assertion matches current product semantics. If the expected behavior is unsettled, resolve it with the requirement owner before encoding it as a regression.
2. Choose the smallest boundary that can observe the risk. Use a package test for a domain invariant, real SQLite for persistence, an HTTP test for an API contract, an owned process for startup or restart, and a real model scenario only for behavior that actually depends on model semantics. A test of a recovery function does not by itself prove process restart behavior.
3. Construct isolated starting state. Trigger the action through the boundary named in the contract. Capture actual outputs and final state. Assert both the required effect and a relevant forbidden effect, including an error or interrupted path when that is the risk.
4. Run the case alone, then the relevant package or component suite, then the checks needed for the PR or release. When feasible for a high-risk regression, confirm the case fails against the known faulty behavior or a deliberately invalid fixture and accepts a valid boundary result; a test that cannot distinguish them is not useful evidence.
5. Record the command, result, tested revision, and any unverified path. If the case cannot be executed, report the reason and remaining risk instead of writing `PASS`.

The contract can be a short note in an Issue, a test name and comments, or a versioned scenario file. Do not add a separate manifest for every ordinary Go test.

## Review Whether a Case Is Ready

Review the case contract before relying on its result. A valid case may report `FAIL` when the product misses its declared behavior; case readiness and a product run's verdict are separate decisions. Check these points against the case README, definition when present, executor, and captured artifacts:

1. **Risk and scope:** Name the requirement and failure this case can detect, check nearby coverage, and state the behavior its result cannot establish.
2. **Entry and setup:** Exercise the product boundary the case claims to cover. Make fixture bytes, permissions, starting state, external dependencies, and isolation reproducible. Do not use a direct database write as proof of an API or user-flow action.
3. **Oracle:** Predeclare required and forbidden outcomes. Show why each assertion follows from the requirement; justify any required internal record or trace and check that another valid product path or boundary input would not be rejected. Identify the input and outcome that each assertion connects, the relevant result set, and whether one matching result suffices, every result must comply, or exactly one is required. A correct result must not hide an additional forbidden result. Distinguish an observed contradiction from missing evidence or failed setup.
4. **Detection and evidence:** Check that the captured output can support every verdict and that the case detects its intended failure. For a programmed oracle, exercise a representative valid boundary result and a known faulty result or controlled bad input when feasible; if several effects can occur, include a mixed correct-and-incorrect result. Record why a check could not be made. A schema-valid result or one successful product run alone does not establish that the oracle is sound.
5. **Execution and review:** Run the case's definition checks and inspect its runner and result validation. Before relying on a new case's result, inspect one complete run record and confirm its commands, limits, cleanup, artifact paths, and unverified claims. Review the case contract as part of the change review; a valid case may remain active when the current product returns `FAIL`.

## Select Verification by Risk

- Identify the changed behavior, its failure modes, and the evidence needed to detect them. Give priority to data integrity, permissions, migrations, asynchronous state, recovery, evidence provenance, and known regressions. A simple reversible change does not need a new test merely to increase a count or coverage percentage.
- Inspect existing tests and execution paths before adding cases. Strengthen a test that already covers the relevant contract; add a case when a material risk remains unverified. Keep a discovered defect as a regression test when its failure can be asserted deterministically.
- Separate three questions: whether an implementation obeys a deterministic contract, whether a model-dependent result is semantically supported, and whether a mechanism produces a sustained effect. Evidence for one question does not establish the others.
- Prefer the cheapest reliable verifier. Use code assertions for rules and stable outcomes. Script a repeatable end-to-end path when its setup and assertions are known. Use a test agent when the task requires semantic interpretation, exploration, or adapting its steps to observed behavior.
- Assign expensive checks to the changes and release risks that need them. Do not make live model calls, broad race runs, cross-platform packaging, or long experiments prerequisites for every local edit.
