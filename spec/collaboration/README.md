# Git Collaboration Workflow: Issue -> main

Each Issue is a coherent deliverable that can be reviewed, verified, and safely merged into `main`. Normally, a contributor creates one temporary Issue branch from `main`, completes the work there, and squash merges it through a PR. The branch is retired after the merge. Local child branches are reserved for the few cases that need them; they do not change the delivery path. Follow [Issue preparation](issue.md) before development and [review and merge requirements](review.md) before merging. See [Git and delivery](git.md) for the workflow and [PR descriptions](pr.md) for the PR format.

## Delivery Boundary

An Issue should define a result that can be independently reviewed, verified, and safely integrated. It may deliver a user feature, a fix, a migration, or infrastructure work. Multiple Issues do not first need to share a separate integration branch.

## Why This Workflow

- Each Issue goes through one PR into `main`. Its final result becomes one commit, while internal development stages do not complicate the mainline history.
- The PR targets the actual integration branch, so reviewers can inspect and verify the relevant diff directly. The Issue's completion state can correspond to what was merged into `main`.
- Work begins from the latest `main`, reducing divergence from long-lived branches and avoiding commit-ID alignment problems after a feature branch is merged and reused.

## Constraints and Tradeoffs

- Every merge directly affects `main`. When a feature spans several Issues, each intermediate deliverable must remain safe to integrate; otherwise, adjust the Issue boundary or postpone the merge.
- There is no shared pre-merge integration branch for several independent Issues. Stages within one Issue can still be combined and verified on its own branch.
- `main` retains one result commit per Issue, so internal stages cannot be located or reverted as separate mainline commits. This cost grows if an Issue is too broad or includes unrelated changes.
- Dependencies and release membership across Issues must be expressed through Issues, PRs, and release records rather than inferred from branch structure. Parallel work still requires timely checks for semantic conflicts.

If the project repeatedly needs to verify several Issues together before any can enter `main`, and their boundaries cannot be adjusted to make them safely integrable, reassess the workflow together. An individual contributor must not switch merge rules unilaterally.
