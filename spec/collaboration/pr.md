# PR Description: Issue -> main

A PR into `main` is the final review and delivery point for one Issue. Describe the **actual diff between the PR and `main`** so reviewers can judge whether the result is coherent, verifiable, and safe to integrate. Intermediate commits are not separate acceptance stages. The requirement boundary and merge conditions are defined in [Issue preparation](issue.md) and [review and merge requirements](review.md).

## Scope and Title

- One PR into `main` carries the final result of one Issue. If supporting changes are necessary, explain how they serve the goal. Use a separate Issue and PR for independent work. Split overly broad scope along independently deliverable results.
- Before opening a PR, fetch the latest `main` and rebase the delivery branch onto it. Resolve conflicts during rebase and inspect the final diff. Do not merge `main` into the branch merely to synchronize it.
- Use a title that summarizes the delivered result, not only an Issue number, version, or vague phrase such as "miscellaneous fixes." Reference the corresponding Issue and relevant design decisions in the body.
- The final PR result must cover the Issue's acceptance scope. If only part is complete, split or revise the Issue first and state what this PR delivers. Use closing keywords such as `Fixes #123` only when the Issue is fully resolved.
- Merge using **Squash and merge**. Edit the squash commit's title and body so they accurately summarize the final result. Explain internal stages in the PR if needed to understand the result; do not rely on them as separate commits on `main`.
- After a rebase, conflict resolution, or requirement change, recheck the PR diff and description. If new logic on `main` conflicts with or supersedes the Issue, discuss and update the requirement boundary before continuing review.

## Body Structure

### Why

Briefly explain the current problem, affected behavior, and intended result, and link the Issue. Answer why the work is needed before listing files or changes.

### Changes

Group changes by behavior or responsibility. Describe what was actually implemented and its important boundaries. Do not list files mechanically or claim that planned work has been completed. Explain material effects on migrations, permissions, asynchronous behavior, prompts, or user-visible interaction.

### Validation

List **checks actually performed** and their results, including platform, environment, or test data when relevant. For a behavior fix, describe the reproduction scenario and the observation after the fix. If reproduction is unreliable, state the verification boundary. Explain relevant checks not run and what remains unverified. After updating the branch, confirm that these results still apply to the final PR diff.

### Review Note

Collect tradeoffs, cost changes, known limitations, dependencies, or merge-order concerns that need special attention from reviewers. Include this section when a change materially affects data migration, permissions, scheduling, model-call cost, or compatibility; otherwise omit it.

## Copyable Template

Replace or remove the bracketed instructions before submitting the PR. List only checks actually performed. Remove optional sections that do not apply.

```markdown
## Why

[Link the Issue. Describe the problem and intended result.]

## Changes

### [Behavior or responsibility]

- [Implemented change and important boundary.]

## Validation

- [Check performed] — [Result; include environment or platform when relevant.]
- [Key behavior or reproduction scenario] — [Observed result; state the boundary if reproduction was unreliable.]
- [Relevant check not performed] — Not run: [Reason and remaining verification boundary.]

## Review Note

[Tradeoffs, cost, risks, dependencies, or merge order needing review; remove if none.]
```

The title, `Why`, `Changes`, and final squash commit should describe the same deliverable. Claims in the PR should be checkable against the final code, test results, or runtime records.
