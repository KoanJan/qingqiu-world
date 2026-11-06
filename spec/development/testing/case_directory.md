# Cross-Component Test Case Directories

This standard defines **where files live** for cross-component cases under `tests/cases/`. Use [deterministic tests](deterministic.md), [semantic scenarios](semantic.md), [experiments](experiments.md), and [evidence and reports](evidence.md) for test behavior and result rules.

## Case Boundary and Name

- Put each case in exactly one track directory: `tests/cases/D/` for deterministic cases, `tests/cases/S/` for semantic cases, and `tests/cases/X/` for mechanism experiments. Do not add another grouping level by component.
- Give each case one self-contained, stable lowercase kebab-case directory name that describes the behavior, such as `D/user-profile-restart/` or `S/rag-source-order/`. Keep the name unique across D, S, and X so the directory name alone identifies the case. The track directory already states the test type; do not add `d-`, `s-`, or `x-` prefixes or ordinal numbers such as `001`. Do not repeat the name as an `id` field in the definition or as a command argument.
- A case owns the files needed to run and understand it. Keep its input files, product workflow, assertions, and case-specific validators in that case, even if another case uses similar input. A product workflow identical across several cases in one track may live once at that track level, with each case's `scripts/run.py` a thin entry point that passes its case directory. Shared [`tools.py`](../../../tests/cases/tools.py) owns report layout, local environment setup, process lifecycle, and HTTP transport; it does not contain fixtures or case verdict rules.
- Keep ordinary package, component, and CI-script tests beside the code they verify. Use `tests/cases/` when the case needs its own cross-component setup, runner, evidence, and operator guide.

## File Roles

| Location | When present | Contents |
| --- | --- | --- |
| `<case>/README.md` | Every case | The entry point for a contributor: run command, prerequisites, data, expected result, and evidence locations. |
| `<case>/scenario-vN.json` or another versioned definition | When the case has a separate definition | The case's input and criteria. Keep the definition at the case root so it is visible before opening implementation files. |
| `<case>/SKILL.md` | Only when the case needs Agent instructions beyond its track's shared method | Case-specific Agent procedure. An S case normally uses the shared [`S/SKILL.md`](../../../tests/cases/S/SKILL.md); a case is not automatically a separate Skill. |
| `<case>/assets/` | Only when committed input files are needed | This case's source documents, fixtures, or other nonsensitive inputs. |
| `<case>/scripts/` | When executable code is needed | Runner, validators, and focused tests of those scripts. Use descriptive filenames; `run.py` is the current runner convention. |
| `<case>/schemas/` | Only when a non-Python consumer needs a declarative schema | Optional declarative schemas. Python case contracts live in `scripts/case_contract.py`. |
| `<case>/reports/` | Created for the first run | Git-ignored, uniquely named run directories with the report layout below. |

Create optional source directories only when they contain committed files. Each run creates a fresh `<case>/reports/<run>/` directory; the repository [ignores these reports](../../../.gitignore). Keep the run root limited to these entries:

~~~text
<case>/reports/<run>/
  result.json    # machine-readable status and evidence index
  report.md      # human-readable conclusion, Status line, and evidence guide
  data/          # copied inputs, product state, logs, traces, and judge evidence
    product/     # application data root when a product process runs
~~~

Create `data/` even if setup fails before starting the product. Put additional run files under `data/`; do not scatter them beside the two report files. A case may organize `data/` further as its evidence requires. Never reuse another case's run directory or put generated output in `assets/` or `schemas/`. The case README tells the contributor how to locate and interpret its runs.

Python runners import `RunRecord` from `tests/cases/tools.py`: construct `RunRecord(CASE_DIR)` at run start, write direct JSON evidence with `write_data_json`. Use `write_case` for the standard report and result fields, or `write_report` and `write_result` when a case needs a custom report. The class creates a fresh `reports/run-*/data/`, derives `case_name` and `run_id`, indexes direct evidence files under `data/`, and rejects missing common fields, a report status that differs from the result, or unexpected files beside `result.json`, `report.md`, and `data/`. Pass a case-specific validator to `write_result` when the track requires more checks. The module also supplies `free_port`, `http_json`, `call_json`, `build_server`, `start_server`, `stop_server`, `check`, `isolated_build_env`, `isolated_server_env`, `provider_endpoint`, and `git_state` for common execution mechanics. Product actions and assertions remain with the case; the shared [S Skill](../../../tests/cases/S/SKILL.md) gives the Agent's semantic testing method.

An executor or validator derives the case name from its directory. `result.json` records that name as `case_name` and the run directory name as `run_id`. Artifact references are paths relative to the run root: `report.md` or files under `data/`. Fixture paths in a definition are relative to the case directory; avoid repeating `tests/cases/<track>/<case>/` inside the definition. Keep criterion IDs only when they are needed to pair individual criteria with result entries.

## Deterministic Cross-Component Case

The current [user-profile-restart](../../../tests/cases/D/user-profile-restart/README.md) has the minimum layout for a fixed, executable end-to-end contract:

~~~text
tests/cases/D/user-profile-restart/
  README.md
  scripts/
    run.py
~~~

The runner contains this case's fixed setup and assertions. Add `assets/` if it needs committed input files. Add a versioned definition only if data or criteria must be maintained separately from the runner; put case-specific validation in `scripts/` when a separate definition needs it. Add `schemas/` only if a non-Python consumer needs a declarative schema. Do not create a manifest merely to follow a directory template. Add `SKILL.md` only if an Agent has a real execution or judgment role.

The track-level [`D/runall.py`](../../../tests/cases/D/runall.py) discovers every direct case directory and runs its `scripts/run.py` from the repository root. A new D case must provide that runner to participate. The aggregate command fails if any case fails, cannot start, or has no runner; it does not replace an individual case's README or evidence.

## Semantic Cross-Component Case

The current [rag-source-order](../../../tests/cases/S/rag-source-order/README.md) shows the layout for a scripted semantic scenario with an independent Agent judgment:

~~~text
tests/cases/S/rag-source-order/
  README.md
  scenario-v1.json
  assets/
    ordering.md
    calibration-claims.md
  scripts/
    run.py
    validate_scenario.py
    validate_result.py
~~~

Keep a scripted scenario's versioned definition at the root, source data in `assets/`, and execution and validation code in `scripts/`. The shared [S Skill](../../../tests/cases/S/SKILL.md) supplies the semantic testing method; each case README supplies its entry point, assets, evidence locations, and operator-only expectations. The current RAG cases reuse [`base_contracts.py`](../../../tests/cases/S/base_contracts.py) for S-track fields and evidence rules and [`rag_contract.py`](../../../tests/cases/S/rag_contract.py) for their workflow's limits and routes. Their validator scripts load optional `scripts/case_contract.py` models and otherwise use the shared RAG scenario model and base result model. Add that file only for case-specific rules, subclassing the relevant shared model. Their `assets/calibration-claims.md` holds unlabeled reference claims for judge calibration, while expected labels stay in the operator README. A scenario executed entirely by an Agent may use a Markdown definition and need no runner, provided its README gives an executable workflow. The semantic standard determines which scenario and result checks are required; this layout does not require copying every file from rag-source-order.

Add a future mechanism experiment under `tests/cases/X/<case-name>/` using the same file roles. The [experiment standard](experiments.md) is preliminary and needs completion through an actual trial; no executable X case exists yet. When adding any case, add its guide to [the case index](../../../tests/cases/README.md) and [the tests index](../../../tests/README.md).
