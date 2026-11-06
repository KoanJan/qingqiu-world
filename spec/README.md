# Qingqiu Project Standards

This directory contains development and collaboration rules that apply across releases. They give contributors a common basis for design review, implementation, and acceptance. Domain design and release-specific decisions belong in their respective design documents.

The adopted Git workflow is **Issue -> main**. These documents describe the project's current standards. This directory is self-contained and does not require links to documents outside `spec/`. If a standard conflicts with established domain semantics or an approved release design, examine the conflict and update the standard; do not change an established design merely to make the standards appear complete.

| Area | Document | Focus |
| --- | --- | --- |
| Development | [General software design](development/general.md) | Responsibilities, dependencies, reuse, complexity, and behavioral contracts |
| Development | [Go project](development/go_project.md) | Domain types, code organization, error handling, and verification |
| Development | [Database](development/database.md) | Data constraints, provenance, migrations, and derived indexes |
| Development | [Web project](development/web_project.md) | UI structure, themes, copy, and interaction |
| Development | [Prompts and tools](development/prompt_and_tools.md) | Information selection, narrative, tool contracts, and result presentation |
| Development | [Test engineering](development/testing/README.md) | Risk-based verification, semantic scenarios, experiments, and evidence |
| Collaboration | [Issue preparation](collaboration/issue.md) | Goals, acceptance, scope, dependencies, and ownership |
| Collaboration | [Local data snapshots](collaboration/local_data.md) | Back up the full data directory before an Issue; restore the migration baseline after rebasing |
| Collaboration | [PR review and merge](collaboration/review.md) | Independent review, verification evidence, and merge requirements |
| Collaboration | [Git workflow](collaboration/README.md) · [Git and delivery](collaboration/git.md) · [PR descriptions](collaboration/pr.md) | One branch per Issue, squash merge into `main`; nested branches are rarely needed |

## How to Apply These Standards

1. **Define responsibility before structure.** A new field, table, tool, or state must have a clear purpose, writer, reader, and lifetime. Reuse an existing domain object when it expresses the same meaning.
2. **Separate rules from implementation choices.** These standards capture boundaries that apply across releases. Numeric thresholds, release-specific schemas, and prompt text belong in code or release designs and may change.
3. **Distinguish settled rules from open questions.** State confirmed constraints directly. Mark disputed directions as open; do not present a theoretical analogy or a single test result as proof.
4. **Accept work using verifiable behavior.** Use executable assertions for deterministic rules. For model-dependent behavior, predefine the scenario and inspect its inputs, outputs, and evidence under the [test engineering standard](development/testing/README.md). Do not use a generative LLM's quality score as proof of correctness.

When revising this directory, check that its documents remain consistent with one another and with current behavior and external documentation. State an effective release boundary where one is needed.
