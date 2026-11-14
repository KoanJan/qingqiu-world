# Cross-Component Test Case Directories

This standard defines **where files live** for cross-component cases under `tests/cases/`. Use [deterministic tests](deterministic.md), [semantic scenarios](semantic.md), [experiments](experiments.md), and [evidence and reports](evidence.md) for test behavior and result rules.

## Case Boundary and Name

- Put each case in exactly one track directory: `tests/cases/D/` for deterministic cases, `tests/cases/S/` for semantic cases, and `tests/cases/X/` for mechanism experiments. Do not add another grouping level by component.
- Give each case one self-contained, stable lowercase kebab-case directory name that describes the behavior. Keep the name unique across D, S, and X so the directory name alone identifies the case. The track directory already states the test type; do not add `d-`, `s-`, or `x-` prefixes or ordinal numbers such as `001`. Do not repeat the name as an `id` field in the definition or as a command argument.
- A case owns the files needed to run and understand it. Keep its input files, product workflow, assertions, and case-specific validators in that case, even if another case uses similar input. A product workflow identical across several cases in one track may live once at that track level, with each case's `scripts/run.ts` a thin entry point that passes its case directory. Shared [`tools.ts`](../../../tests/cases/tools.ts) owns report layout, local environment setup, process lifecycle, and HTTP transport; it does not contain fixtures or case verdict rules.
- Keep ordinary package, component, and CI-script tests beside the code they verify. Use `tests/cases/` when the case needs its own cross-component setup, runner, evidence, and operator guide.

## File Roles

| Location | When present | Contents |
| --- | --- | --- |
| `<case>/README.md` | Every case | The entry point for a contributor: run command, prerequisites, data, expected result, and evidence locations. |
| `<case>/scenario.json` or another definition | When the case has a separate definition | The case's input and criteria. Keep the definition at the case root so it is visible before opening implementation files. |
| `<case>/SKILL.md` | Only when the case needs Agent instructions beyond its track's shared method | Case-specific Agent procedure. An S case normally uses the shared [`S/SKILL.md`](../../../tests/cases/S/SKILL.md); a case is not automatically a separate Skill. |
| `<case>/assets/` | Only when committed input files are needed | This case's source documents, fixtures, or other nonsensitive inputs. |
| `<case>/scripts/` | When executable code is needed | Runner, validators, and focused tests of those scripts. An implemented case uses `run.ts` as its entry point, `prepare_data.ts` for starting product state, and `execute_test.ts` for the tested action and observations. Identical workflows may delegate to track-level implementations. |
| `<case>/schemas/` | Only when another consumer needs a declarative schema | Optional declarative schemas. TypeScript case contracts live in `scripts/case_contract.ts`. |
| `<case>/reports/` | Created for the first run | Git-ignored, uniquely named run directories with the report layout below. |

Create optional source directories only when they contain committed files. Each run creates a fresh `<case>/reports/<run>/` directory; the repository [ignores these reports](../../../.gitignore). Keep the run root limited to these entries:

~~~text
<case>/reports/<run>/
  result.json    # machine-readable status and evidence index, when a valid result exists
  report.md      # human-readable conclusion, Status line, and evidence guide
  data/          # copied inputs, product state, logs, traces, and judge evidence
    product/     # application data root when a product process runs
~~~

Create `data/` even if setup fails before starting the product. Put additional run files under `data/`; do not scatter them beside the two report files. A case may organize `data/` further as its evidence requires. Never reuse another case's run directory or put generated output in `assets/` or `schemas/`. The case README tells the contributor how to locate and interpret its runs. A run that fails before a valid result record can be assembled, such as an invalid scenario definition, still writes `report.md` and `data/`; the report explains the diagnostic state, and omitting `result.json` keeps an incomplete record from claiming a status or evidence index. A failure after the definition validates, including input preparation or execution, writes a diagnostic `ERROR` result when the record can satisfy its track contract.

TypeScript runners use `RunRecord` from the shared [`tools.ts`](../../../tests/cases/tools.ts) to create a fresh run directory, derive case identity, enforce report layout and status agreement, and apply any required track or case validator before claiming a final result. Product actions and assertions remain with the case. The [case index](../../../tests/cases/README.md) documents the shared API.

An executor or validator derives the case name from its directory. `result.json` records that name as `case_name` and the run directory name as `run_id`. Artifact references are paths relative to the run root: `report.md` or files under `data/`. Fixture paths in a definition are relative to the case directory; avoid repeating `tests/cases/<track>/<case>/` inside the definition. Keep criterion IDs only when they are needed to pair individual criteria with result entries.

## Deterministic Cross-Component Case

A D case has `README.md` and `scripts/run.ts`. The runner coordinates its preparation, execution, cleanup, and report; `prepare_data.ts` and `execute_test.ts` own the corresponding case actions and checks. The [hypothetical D case](deterministic.md#one-hypothetical-d-case) shows the minimum layout and evidence. Add `assets/` if it needs committed input files. Add a versioned definition only if data or criteria must be maintained separately from the runner; put case-specific validation in `scripts/` when a separate definition needs it. Add `schemas/` only if another consumer needs a declarative schema. Do not create a manifest merely to follow a directory template. Add `SKILL.md` only if an Agent has a real execution or judgment role.

The track-level [`D/runall.ts`](../../../tests/cases/D/runall.ts) discovers every direct case directory and invokes its `scripts/run.ts` through the tests package. A new D case must provide that runner to participate. The aggregate command fails if any case fails, cannot start, or has no runner; it does not replace an individual case's README or evidence.

## Semantic Cross-Component Case

Keep a scripted S case's `scenario.json` at its root, committed inputs in `assets/` when needed, and execution and validation code in `scripts/`. The [hypothetical S case](README.md#one-hypothetical-s-case) illustrates this layout without making one workflow's assets mandatory for another. The shared [S Skill](../../../tests/cases/S/SKILL.md) supplies the semantic testing method; each case README supplies its entry point, inputs, evidence locations, and operator-only expectations. Add `scripts/case_contract.ts` when the available shared model cannot express the case's input or result rules; omit it when no case-specific rules are needed. Do not impose one workflow's model on another. Keep blind calibration claims under `assets/` only when calibration uses committed inputs, and keep expected labels in operator material rather than material shown to the judge. A scenario executed entirely by an Agent may use a Markdown definition and need no runner, provided its README gives an executable workflow. The semantic standard determines which scenario and result checks are required.

Add a mechanism experiment under `tests/cases/X/<case-name>/` using the same file roles. The [experiment standard](experiments.md) is preliminary and needs completion through an actual trial. When adding any case, add its guide to [the case index](../../../tests/cases/README.md) and [the tests index](../../../tests/README.md).
