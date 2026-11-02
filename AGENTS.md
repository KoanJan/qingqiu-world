# Project Standards by Work Stage

The project-wide standards are indexed in [spec/README.md](spec/README.md). This file points to the standards relevant to each stage of work; it does not assign stages to people or agents. The current task determines which stages are needed and who performs them. Listing a stage here does not require it to be performed for every task.

| Work stage | Applicable standards |
| --- | --- |
| Define or revise an Issue's goal, scope, and acceptance criteria | [Issue preparation](spec/collaboration/issue.md) |
| Create a branch, synchronize with `main`, or integrate a change | [Git and delivery](spec/collaboration/git.md) and the [collaboration overview](spec/collaboration/README.md) |
| Prepare local data or restore it after a rebase | [Local data during Issue development](spec/collaboration/local_data.md) |
| Design, implement, or change behavior | [General software design](spec/development/general.md), plus the standards for the affected area: [Go](spec/development/go_project.md), [database](spec/development/database.md), [Web](spec/development/web_project.md), or [prompts and tools](spec/development/prompt_and_tools.md) |
| Verify an implementation | The verification requirements in the applicable [development standards](spec/README.md) and [PR review and merge](spec/collaboration/review.md) |
| Describe a PR | [PR descriptions](spec/collaboration/pr.md) |
| Review, approve, or merge a PR | [PR review and merge](spec/collaboration/review.md) and [Git and delivery](spec/collaboration/git.md) |

Release-specific designs and the current request determine the intended behavior. The linked standards govern how the applicable work is carried out; detailed rules live in `spec/` rather than being duplicated here.
