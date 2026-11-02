# Preparing an Issue

An Issue defines a shared boundary for development and acceptance. It describes the result to deliver without prescribing every implementation detail. The project uses the [Issue -> main](git.md) workflow: each Issue is delivered through a temporary branch and a PR.

## Minimum Information

| Item | Question to answer |
| --- | --- |
| Goal | What problem or need does this address, and what observable result should completion produce? |
| Acceptance | Which scenarios, inputs, and results show that the goal has been met? Give checkable conditions for deterministic behavior. For model-dependent behavior, specify the scenario and evidence. Do not use a generative LLM to score it with natural language. |
| Scope | Which changes are required? Which adjacent problems remain for later? State exclusions when omitting them could cause confusion. |
| Dependencies | Does this depend on another unmerged Issue, a design decision, or an external resource? Who owns each prerequisite? |
| Owners | Who drives implementation, and who confirms the scope and acceptance result? One person may hold both roles, but the PR still needs review by someone other than its author. |

Before development starts, the implementation owner should be able to identify the delivery boundary and confirm that no unresolved question blocks the work. Clarify uncertain acceptance criteria or dependencies with the person responsible for the requirement; do not fill in a product decision without discussion.

## Changes During Development

- If new logic on the target branch conflicts with the requirement, the acceptance scope changes, or a separate goal becomes necessary, confirm the revised scope with its owner before updating the Issue and PR. Existing code does not by itself justify a new requirement.
- Split an overly broad Issue along independently deliverable and verifiable results. Implementation steps can remain in commits or internal tasks; they do not each require a new Issue.
- Acceptance criteria describe final behavior. Review may change the implementation approach as long as it does not silently change the delivery goal, data boundary, or external behavior.
