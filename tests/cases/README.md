# Test Cases

Each cross-component case has a self-contained directory under one track. Begin with its README and follow the [case directory standard](../../spec/development/testing/case_directory.md) for file placement.

## TypeScript environment

Use Node.js 20.19+ (20.x) or 22.12+. Install the pinned test dependencies once from the repository root:

~~~bash
npm --prefix tests ci
~~~

Run a case entry point from the repository root with `npm --prefix tests run case -- cases/<track>/<case>/scripts/<script>.ts`. Script paths and relative file arguments resolve from `tests/`; use `cases/...` or an absolute path. Type-check with `npm --prefix tests run typecheck` and run offline utility and contract tests with `npm --prefix tests run test:unit`. The TypeScript entry point uses `tsx`; SQLite evidence uses `sqlite3`; S contract models use Zod. `npm ci` installs these through the test package.

Every live S-case run requires **six environment variables: three for the LLM and three for the embedding provider**. The [semantic standard](../../spec/development/testing/semantic.md#result-limits-and-execution-boundary) lists each variable and its purpose. The shared [provider preflight](S/provider_env.ts) checks all six before any product action; missing or blank values produce a diagnostic `ERROR`. Definition validation and offline contract tests need no provider credentials.

| Directory | Kind of result | Running premise and evidence standard |
| --- | --- | --- |
| [`D/`](D/README.md) | Deterministic check | Fixed assertions; normally no live model. Run all cases with `npm --prefix tests run test:d`; a passing runner establishes its stated contract. See [deterministic tests](../../spec/development/testing/deterministic.md). |
| [`S/`](S/SKILL.md) | Semantic judgment | Real model configuration and inspection of original evidence; an independent judge is preferred, and provider calls can cost money. A capture alone is not `PASS`. Follow the shared [S Skill](S/SKILL.md) and [semantic standard](../../spec/development/testing/semantic.md). |
| [`X/`](X/README.md) | Mechanism experiment; future work | No executable case or trial-verified method yet. Future cases need comparable conditions and repeated runs, often with model cost. The [experiment standard](../../spec/development/testing/experiments.md) is preliminary and must be completed through an actual case. |

| Case guide | Behavior |
| --- | --- |
| [background-upload-restart](D/background-upload-restart/README.md) | Upload a background, read identical bytes after restart, reject an invalid extension, and delete it. |
| [user-profile-restart](D/user-profile-restart/README.md) | Preserve user identity and an updated bio across a real server restart. |
| [rag-source-order](S/rag-source-order/README.md) | Infer the third disciple from an ordered original record. |
| [rag-insufficient-evidence](S/rag-insufficient-evidence/README.md) | Acknowledge that the original record does not identify a third disciple. |
| [schedule-reminder-from-chat](S/schedule-reminder-from-chat/README.md) | Create a scheduled reminder that matches a chat request's future time and content. |
| [relay-message-to-person](S/relay-message-to-person/README.md) | Deliver a user's relay to the named Person's session and preserve its key information. |

## Current implementation notes

TypeScript runners use `RunRecord` in [`tools.ts`](tools.ts) to create a fresh `reports/<run>/data/`, derive `case_name` and `run_id`, write `result.json` and `report.md`, index evidence, and reject a mismatched report status or unexpected run-root files. Construct it with the case directory; use `writeDataJson` for direct JSON evidence, `writeCase` for a standard D result, or `writeReport` and `writeResult` for a custom result. Pass a case validator to `writeResult` when needed. The same module supplies `freePort`, `httpJson`, `callJson`, `buildServer`, `startServer`, `stopServer`, `check`, `isolatedBuildEnv`, `isolatedServerEnv`, and `providerEndpoint`. Product actions and assertions stay in the case runner. Each run builds the server from current local source files.

The current scripted S cases use `scenario.json`, local scenario and result validators, and `data/capture.json` to pin mechanical facts before judgment. Their result validators compare `mechanical_failures`, `mechanical_checks_performed`, and `observed_model_calls` against the capture record and preserve a captured `FAIL` or `ERROR`. Capture runners return `3` for a completed capture awaiting judgment, `1` for mechanical `FAIL`, and `2` for `ERROR`. Final result validation returns `0` only for a valid `PASS`, `1` for a valid final non-`PASS` or diagnostic `ERROR`, and `2` for an incomplete or invalid result. `--capture` checks only the scaffold and returns `1` when valid. The case README gives its exact commands and evidence.

| S workflow | Definition and evidence shape |
| --- | --- |
| [RAG source order](S/rag-source-order/README.md) and [insufficient evidence](S/rag-insufficient-evidence/README.md) | `fixture` and `question`, shared [`base_contracts.ts`](S/base_contracts.ts) and [`rag_contract.ts`](S/rag_contract.ts), with no case-specific contract file. The source is copied into the run and checked by hash; the case README defines its count semantics, and its validator requires a positive observed work-interaction count for `PASS`. |
| [Schedule reminder](S/schedule-reminder-from-chat/README.md) | A chat `request`, expected delay, and tolerance. Its `scripts/case_contract.ts` supplies workflow-specific scenario and result models; [`chat_contract.ts`](S/chat_contract.ts) supplies shared evidence rules. |
| [Message relay](S/relay-message-to-person/README.md) | A chat `request` and target person name. Its `scripts/case_contract.ts` supplies workflow-specific scenario and result models; [`chat_contract.ts`](S/chat_contract.ts) supplies shared evidence rules. |

## Add a case

1. Use the [test engineering contract](../../spec/development/testing/README.md#implement-one-test) to define the risk, real product entry point, setup, action, oracle, and evidence. Check existing coverage first. Keep ordinary package tests beside the package; add a directory here for a cross-component case.
2. Create `tests/cases/<D|S|X>/<case-name>/` using the [directory standard](../../spec/development/testing/case_directory.md). Name it for the behavior, without a track prefix or sequence number, and keep it unique across tracks. Its README must contain the exact commands, environment prerequisites, data, expected and forbidden outcomes, evidence to inspect, and result meanings. A contributor should be able to run the case without another case's files. For a TypeScript runner, use shared [`tools.ts`](tools.ts) for report output, process lifecycle, HTTP JSON transport, port allocation, and isolated build/server environments; keep the test's product actions, assertions, and verdict rules in its own `scripts/`. A D case also provides `scripts/run.ts` so the [aggregate runner](D/README.md) can execute it.
3. For a semantic case, follow the [semantic standard](../../spec/development/testing/semantic.md) and shared [S Skill](S/SKILL.md). Put its actual input, criteria, and limits in `scenario.json`; describe its product entry point, execution method, assets, isolation, and evidence in its README. Its live executor must call the shared [`missingProviderEnv`](S/provider_env.ts) preflight before product setup and record missing variables as a diagnostic `ERROR`. The executor must exercise the declared entry point and load the definition rather than duplicate it. The current [rag-source-order](S/rag-source-order/README.md) and [rag-insufficient-evidence](S/rag-insufficient-evidence/README.md) cases use a `scenario.json`, [`S/base_contracts.ts`](S/base_contracts.ts), and the RAG-specific [`S/rag_contract.ts`](S/rag_contract.ts). Their `scripts/case_contract.ts` may be omitted when they have no extra rules. Add another case using this RAG runner to [`S/test_base_contracts.ts`](S/test_base_contracts.ts). A case with another product workflow uses its own definition, executor, and contract checks; it need not adopt the RAG model or files.
4. Review the proposed case against the [case readiness checks](../../spec/development/testing/README.md#review-whether-a-case-is-ready). After its definition validates and its capture and result checks are implemented, run the case, inspect a complete result and evidence path, and record any unverified detection check. A product `FAIL` can be a valid outcome of this review. For a scripted RAG case named `rag-conflicting-sources`, the commands would be:

   ~~~bash
   npm --prefix tests run case -- cases/S/rag-conflicting-sources/scripts/validate_scenario.ts
   npm --prefix tests run case -- cases/S/rag-conflicting-sources/scripts/run.ts
   ~~~

   These commands describe the current scripted RAG cases; another S case may use a different scenario format or executor. Use that case's README for its data, commands, and expected outcomes, and the shared [S Skill](S/SKILL.md) for the execution and judgment method. A completed capture is not a semantic `PASS`; inspect the final record and its evidence before relying on the case.
5. For a deterministic cross-process case, follow the [deterministic standard](../../spec/development/testing/deterministic.md) and include its command and oracle in its README. Future X cases can start from the [preliminary experiment principles](../../spec/development/testing/experiments.md), but their method and evidence rules need completion and review before relying on a result. Update this index and [tests/README.md](../README.md) after adding any case.
