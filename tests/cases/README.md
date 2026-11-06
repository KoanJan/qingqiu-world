# Test Cases

Each cross-component case has a self-contained directory under one track. Begin with its README and follow the [case directory standard](../../spec/development/testing/case_directory.md) for file placement.

## Python environment

The S contract models use Pydantic. Create an isolated environment once from the repository root; do not install into the system interpreter:

~~~bash
python3 -m venv tests/cases/.venv
tests/cases/.venv/bin/pip install -r tests/cases/requirements.txt
~~~

Run S validators and runners with `tests/cases/.venv/bin/python`. D cases and [`tools.py`](tools.py) remain standard-library only.

Every live S-case run requires the six LLM and embedding environment variables listed in the [semantic standard](../../spec/development/testing/semantic.md#result-limits-and-execution-boundary). The shared [provider preflight](S/provider_env.py) checks their presence before any product action; missing or blank values produce a diagnostic `ERROR`. Definition validation and offline contract tests need no provider credentials.

| Directory | Kind of result | Running premise and evidence standard |
| --- | --- | --- |
| [`D/`](D/README.md) | Deterministic check | Fixed assertions; normally no live model. Run all cases with `python3 tests/cases/D/runall.py`; a passing runner establishes its stated contract. See [deterministic tests](../../spec/development/testing/deterministic.md). |
| [`S/`](S/SKILL.md) | Semantic judgment | Real model configuration and independent inspection of original evidence; provider calls can cost money. A capture alone is not `PASS`. Follow the shared [S Skill](S/SKILL.md) and [semantic standard](../../spec/development/testing/semantic.md). |
| [`X/`](X/README.md) | Mechanism experiment; future work | No executable case or trial-verified method yet. Future cases need comparable conditions and repeated runs, often with model cost. The [experiment standard](../../spec/development/testing/experiments.md) is preliminary and must be completed through an actual case. |

| Case guide | Behavior |
| --- | --- |
| [background-upload-restart](D/background-upload-restart/README.md) | Upload a background, read identical bytes after restart, reject an invalid extension, and delete it. |
| [user-profile-restart](D/user-profile-restart/README.md) | Preserve user identity and an updated bio across a real server restart. |
| [rag-source-order](S/rag-source-order/README.md) | Infer the third disciple from an ordered original record. |
| [rag-insufficient-evidence](S/rag-insufficient-evidence/README.md) | Acknowledge that the original record does not identify a third disciple. |
| [schedule-reminder-from-chat](S/schedule-reminder-from-chat/README.md) | Create a scheduled reminder that matches a chat request's future time and content. |
| [relay-message-to-person](S/relay-message-to-person/README.md) | Deliver a user's relay to the named Person's session and preserve its key information. |

## Add a case

1. Use the [test engineering contract](../../spec/development/testing/README.md#implement-one-test) to define the risk, real product entry point, setup, action, oracle, and evidence. Check existing coverage first. Keep ordinary package tests beside the package; add a directory here for a cross-component case.
2. Create `tests/cases/<D|S|X>/<case-name>/` using the [directory standard](../../spec/development/testing/case_directory.md). Name it for the behavior, without a track prefix or sequence number, and keep it unique across tracks. Its README must contain the exact commands, environment prerequisites, data, expected and forbidden outcomes, evidence to inspect, and result meanings. A contributor should be able to run the case without another case's files. For a Python runner, use shared [`tools.py`](tools.py) for report output, process lifecycle, HTTP JSON transport, port allocation, and isolated build/server environments; keep the test's product actions, assertions, and verdict rules in its own `scripts/`. A D case also provides `scripts/run.py` so the [aggregate runner](D/README.md) can execute it.
3. For a semantic case, follow the [semantic standard](../../spec/development/testing/semantic.md) and shared [S Skill](S/SKILL.md). Put its actual input, criteria, and limits in a versioned definition; describe its product entry point, execution method, assets, isolation, and evidence in its README. Its live executor must call the shared [`missing_provider_env`](S/provider_env.py) preflight before product setup and record missing variables as a diagnostic `ERROR`. The executor must exercise the declared entry point and load the definition rather than duplicate it. The current [rag-source-order](S/rag-source-order/README.md) and [rag-insufficient-evidence](S/rag-insufficient-evidence/README.md) cases use a `scenario-v1.json`, [`S/base_contracts.py`](S/base_contracts.py), and the RAG-specific [`S/rag_contract.py`](S/rag_contract.py). Their `scripts/case_contract.py` may be omitted when they have no extra rules. Add another case using this RAG runner to [`S/test_base_contracts.py`](S/test_base_contracts.py). A case with another product workflow uses its own definition, executor, and contract checks; it need not adopt the RAG model or files.
4. Review the proposed case against the [case readiness checks](../../spec/development/testing/README.md#review-whether-a-case-is-ready). For a versioned S definition, mark it `reviewed` after its requirement, entry point, and oracle have been checked in change review; mark it `active` after its definition validates and its capture and result checks are implemented. Run the new active case, inspect a complete result and evidence path, and record any unverified detection check. A product `FAIL` can be a valid outcome of this review. For a scripted RAG case named `rag-conflicting-sources`, the commands would be:

   ~~~bash
   tests/cases/.venv/bin/python tests/cases/S/rag-conflicting-sources/scripts/validate_scenario.py --file tests/cases/S/rag-conflicting-sources/scenario-v1.json
   tests/cases/.venv/bin/python tests/cases/S/rag-conflicting-sources/scripts/validate_scenario.py
   tests/cases/.venv/bin/python tests/cases/S/rag-conflicting-sources/scripts/run.py
   ~~~

   The first command works while the case is still `proposed`; the last two require `active`. These commands describe the current scripted RAG cases; another S case may use a different scenario format or executor. Use that case's README for its data, commands, and expected outcomes, and the shared [S Skill](S/SKILL.md) for the execution and judgment method. A completed capture is not a semantic `PASS`; inspect the final record and its evidence before relying on the case.
5. For a deterministic cross-process case, follow the [deterministic standard](../../spec/development/testing/deterministic.md) and include its command and oracle in its README. Future X cases can start from the [preliminary experiment principles](../../spec/development/testing/experiments.md), but their method and evidence rules need completion and review before relying on a result. Update this index and [tests/README.md](../README.md) after adding any case.
