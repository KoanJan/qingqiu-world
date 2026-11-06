# PR Review and Merge Requirements

These requirements apply to PRs in the [Issue -> main](git.md) workflow. Use the [PR description standard](pr.md) to describe the work. The conditions below determine when a change may enter `main`.

## Requesting Review

- A Draft PR may be opened for early discussion while the requirement or verification is incomplete. When requesting formal review, the PR must correspond to a confirmed Issue scope and describe the final diff, verification performed, and boundaries needing review.
- The author first synchronizes with the target branch, inspects the final diff, and ensures the reported verification applies to the current code. If a relevant check was not run, state why and what remains unverified.

## Review Responsibilities

- At least one reviewer **other than the PR author** must approve the final diff. The reviewer checks the Issue's acceptance criteria, out-of-scope behavior, and whether code, data, permissions, asynchronous paths, and documentation match the description. Focus on the risks of the actual change; do not mechanically repeat every check for every PR.
- For a case used to verify an acceptance criterion, a non-author reviewer checks out the code being reviewed and runs the relevant committed case locally. Record that run's command, revision, result, and limitations in the PR; generated case reports stay local and Git-ignored. An author's earlier successful run does not override a later product failure observed in the reviewer's run. Investigate environmental or configuration failures separately from product failures.
- The requirement's acceptance owner confirms whether the delivered result meets the Issue goal. A reviewer may also serve as the acceptance owner. Code approval alone does not establish that the product goal has been met.
- Resolve blocking comments before merging. If behavior or risk changes after approval, a non-author reviewer must review the final diff again. Even when a rebase leaves the effective diff unchanged, confirm that earlier verification still applies.

## Merge Requirements

- Required checks for the target branch pass, and the Issue's key acceptance criteria have verification results or reproducible evidence. If a check can genuinely run only after merge, explain why in the PR and name its owner and timing. The acceptance owner and reviewer must confirm that deferring it does not block this merge.
- After the merge, the target branch must still satisfy existing build, runtime, and data compatibility requirements. In particular, a later unfinished Issue must not leave an existing entry point or persisted data unusable on `main`. A clean squash history alone does not establish safety.
- When merging a PR on GitHub, the approver must select **Squash and merge** and comply with repository protection rules. Do not bypass required review or checks. If the PR cannot be squash merged, stop and ask the developer to adjust the code and PR, then review the result again; do not switch merge methods to bypass the problem. Update Issue progress according to the work actually merged.
