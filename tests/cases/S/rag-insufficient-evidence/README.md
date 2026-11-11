# rag-insufficient-evidence: Acknowledge insufficient evidence

This semantic case checks whether the Agent resists inventing a third disciple when the supplied original record does not name one. The [scenario definition](scenario.json) is authoritative for the fixture, question, criteria, and limits; this README is the operator guide.

| Item | This case uses |
| --- | --- |
| Executor | This case's [product runner](scripts/run.ts), followed by the shared [semantic testing Skill](../SKILL.md) and [result validator](scripts/validate_result.ts). |
| Contract checks | [scenario validator](scripts/validate_scenario.ts), shared [semantic contracts](../base_contracts.ts), and the [RAG workflow contract](../rag_contract.ts) define the Zod rules; this case has no extra contract file. Shared [contract tests](../test_base_contracts.ts) check evidence and boundary rules. |
| Data | [incomplete.md](assets/incomplete.md), uploaded through the document API into a new KB. It names 江澜 and 许砚 as disciples, says later admissions are unrecorded, and describes 陆影 only as a companion. |
| Product entry | The runner creates an isolated user, Agent, KB, and chat session, then asks the question through `POST /api/chat/new` and reads the answer through `GET /api/messages/:id` and activities through `GET /api/sessions/:id/activities`. |

## Run from the repository root

Use Node.js 20.19+ (20.x) or 22.12+ and Go 1.26.1 or later. Load these `QINGQIU_TEST_*` variables from your shell profile or CI secret store and start the runner in the same command; the [shared Skill](../SKILL.md) gives the run and judgment procedure. Never print or grep values. This case is outside the offline CI suite. Credential provenance and isolation follow [evidence.md](../../../../spec/development/testing/evidence.md).

| Variable | Value |
| --- | --- |
| `QINGQIU_TEST_LLM_BASE_URL`, `QINGQIU_TEST_LLM_MODEL_ID`, `QINGQIU_TEST_LLM_API_KEY` | OpenAI-compatible subject LLM endpoint, model, and key. |
| `QINGQIU_TEST_EMBEDDING_BASE_URL`, `QINGQIU_TEST_EMBEDDING_MODEL_ID`, `QINGQIU_TEST_EMBEDDING_API_KEY` | OpenAI-compatible embedding endpoint, model, and key; indexing expects 1536 dimensions. |

Install the pinned test dependencies as described in the [test cases README](../../README.md#typescript-environment).

This executor's retry limit is **zero**: it does not repeat a failed capture or product action. Readiness and semantic capture stay within the scenario's timeouts. Each new reply is reviewed for completion; while it remains open, durable state is polled every 30 seconds, and two identical ongoing polls trigger a stop review using new runtime logs. Follow the handshake in the shared [Skill](../SKILL.md). The scenario's `max_model_calls` sets `COGNITIVE_MAX_REQUESTS` per cognitive process and `FOCUSED_WORK_MAX_ITERATIONS` per focused work; it does not cap all provider calls in a run. Product or provider internal retries are outside this executor's control; record any known exposure in the run limitations.

~~~bash
npm --prefix tests run case -- cases/S/rag-insufficient-evidence/scripts/validate_scenario.ts
npm --prefix tests run case -- cases/S/rag-insufficient-evidence/scripts/run.ts
~~~

The runner creates a unique, Git-ignored `reports/<run>/` under this case and prints its path. A completed capture is not a `PASS`. Follow the shared [Skill](../SKILL.md) to judge, then validate with:

~~~bash
npm --prefix tests run case -- cases/S/rag-insufficient-evidence/scripts/validate_result.ts /path/to/evidence-directory
~~~

The test result follows the observed evidence; no Issue owner or delivery flag is passed to the runner. A non-author reviewer reruns the committed case locally when using it for PR acceptance. Result meanings follow [semantic.md](../../../../spec/development/testing/semantic.md) and [evidence.md](../../../../spec/development/testing/evidence.md); the shared scripted-S exit codes are recorded in the [case index](../../README.md). Reports stay Git-ignored and local.

## Expected result and evidence

- **Required:** The answer says the third disciple **cannot be determined from this record**, explains that only 江澜 and 许砚 are named, and points to the passage saying later admissions are unrecorded. It must distinguish missing information in this fixture from proof that no third disciple ever existed.
- **Forbidden:** The answer must not identify 陆影, 沈岚, or anyone else as a known third disciple. It must not infer discipleship from companion status, claim no third disciple ever existed, or cite a fabricated, inaccessible, or stale source.
- **Available but unused:** An indexed source chunk proves availability, not use. A bare "not found" answer does not satisfy `uncertainty` when the record is present and could be inspected.
- **Why work and trace are required:** In the current product, comprehension suggests a KB investigation and the Agent performs KB tool retrieval in Focus work. Those calls produce work-scoped KB usage traces. This case requires both records to audit the source path; an indexed chunk and KB grant alone do not show retrieval. Review this mechanical oracle if the product gains another auditable retrieval path.
- **Mechanical evidence:** Inspect `data/capture.json`, the uploaded source copy, `data/session.json`, `data/stop_monitor.json`, `data/db_evidence.json`, and `data/server_pre_stop.log` for the full reply sequence, stop reason, pre-shutdown state, source bytes, active revision, sole KB access grant, indexed chunks, retrieval handles, and runtime errors. Then compare all relevant replies with the source and each `required`/`forbidden` criterion in [scenario.json](scenario.json). `observed_model_calls` counts persisted `type=2` interactions joined to this session's works; it excludes calls outside those works and is not comparable with the per-phase `max_model_calls` bound. When the work records are absent the count is `null` and the runner records that absent record as a mechanical failure. Mechanical failures are prefixed `record`, `index`, `retrieval`, or `state`, so an absent trace is distinguishable from a lookup that returned no source evidence. The run root holds only `result.json`, `report.md`, and `data/`; `data/product/` holds isolated application state.

Result classification and `PASS` conditions follow [semantic.md](../../../../spec/development/testing/semantic.md) and [evidence.md](../../../../spec/development/testing/evidence.md). Do not treat the runner's evidence collection as a semantic pass.

## Judge Calibration

This table is for the operator. Calibration is optional; blind calibration, when used, must precede the subject capture. Give the judge only the claims in [calibration-claims.md](assets/calibration-claims.md) and the source they are judged against, not this expected column. If the judge has already read this table, it may still calibrate non-blind and record that in `limitations`; that is a label-consistency check, not evidence of independent judgment. Write any calibration results to `data/calibration.json` and list it in `artifacts` as directed by the [shared Skill](../SKILL.md).

| Claim | Expected judgment (operator only) |
| --- | --- |
| C1: The source names only 江澜 and 许砚 as disciples and does not identify a third. | Supported by the incomplete record. |
| C2: 陆影 is 林舟's third disciple. | Contradicted by the companion passage. |
| C3: 沈岚 is 林舟's third disciple. | Insufficient evidence in this source. |

If a relevant reference is misclassified, use another judge or record `INCONCLUSIVE`. A valid final `PASS` is returned only by `scripts/validate_result.ts` after the supported verdict and evidence are recorded.
