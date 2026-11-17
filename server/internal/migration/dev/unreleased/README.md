# Unreleased developer migrations

This package manages committed, persistent data changes that apply only to
developer installations. They run on top of the released baseline (currently
`0.1.21`), before `database.Init()` and before any business service starts, and
they never enter `db_versions`.

The lifecycle itself lives one level up in
[`internal/migration/dev`](../README.md); this directory only declares *what*
changes exist and *how* to execute them.

```
internal/migration/dev/unreleased/
├── README.md        # this guide
├── registry.go      # ordered registry: the single authority
├── steps.go         # Step type, validation, execution
└── scripts/
    ├── 1.sql        # revision 1 data change
    └── .gitkeep     # keeps the embedded directory present when empty
```

## When a step is required

Add a step for any committed change that alters persistent data:

- schema, column, index, or seed changes;
- data backfills, configuration rows, prompt/agent records;
- file or directory data under `DATA_ROOT` (knowledge base, avatars, speech
  assets, vector stores) that cannot be expressed as SQL.

Ordinary code commits that leave persistent data untouched need no step.
Unreleased versions must never be written to `db_versions`, and a development
step must never be added to a release migration chain.

## Adding a step

1. Implement the change. Prefer SQL; use a Go conversion only for work SQL
   cannot express (filesystem, JSON rewriting, external formats).
2. Put the files under `scripts/`, named by revision: `2.sql`, `2.go`.
3. Append exactly one entry to `registered` in `registry.go`:

   ```go
   {
       BaseRelease:  "0.1.21", // must equal config.AppVersion
       FromRevision: 1,        // previous revision
       Revision:     2,        // contiguous, starts at 1
       Description:  "Backfill conversation summaries",
       SQLPath:      "scripts/2.sql",   // optional, "" when no SQL
       // Convert: scripts.Convert2,     // optional, nil when no Go work
   },
   ```

4. Commit the registry entry and every step file in the same commit. A step may
   never be split across commits; its identity depends on the introducing
   commit.
5. Verify:

   ```sh
   cd server
   go test ./internal/migration/dev/...
   ```

6. Restart the development server through the normal launcher. It rebuilds the
   binary, stamps `main.buildCommit`, and applies only the missing revisions.

## Local iteration

Automatic data management only runs on committed code, so a dirty worktree —
tracked or untracked — stops startup before anything is applied. Develop with a
single work-in-progress commit and amend it between runs:

```sh
git commit -m WIP        # first run
git commit --amend       # every later iteration
```

Amending the commit that introduced a step changes its identity, so that step
and everything after it replay from a checkpoint or the released baseline. This
is expected; do not try to keep the old active data by editing state files.
Ordinary commits that leave `unreleased/` untouched keep the chain and reuse
active data as it is. See the lifecycle guide's “Local debugging and uncommitted
changes” for the full breakdown.

## Rules the code enforces

- **The registry is the only authority** over order, revision numbers, and what
  executes. File names only make a revision recognizable; they never decide
  execution. A file that no entry registers is ignored, never executed, and
  never tracked as identity.
- A step must ship SQL, a Go conversion, or both. Neither means validation
  fails before anything runs.
- `SQLPath` is optional. When set, it must be `scripts/<revision>.sql` and the
  embedded file must exist; declaring it makes a missing file fatal.
- `Convert` is referenced directly (`Convert: scripts.Convert2`), so the Go
  compiler guarantees the function exists; no runtime file check is needed.
- SQL must be non-empty and end with a semicolon. Its statements and the
  conversion run in one database transaction against the staged data copy.
- The conversion receives the staged `DATA_ROOT`. Database writes and file
  writes share that transaction only for SQL: file changes are not rolled back
  by a SQLite rollback. On failure the staged copy is abandoned and the retry
  starts from a verified checkpoint or the released baseline, never from a
  half-applied step.
- Every `SourceFiles()` of a step must be introduced by one commit. Editing a
  file after introduction stops the flow with `rewritten after introduction`;
  add a new revision instead of editing a completed one.
- Revision numbers must stay contiguous starting at 1 and must not be
  renumbered. Reordering or rewriting a step changes the chain identity and
  forces a rebuild from the baseline or a checkpoint.
- Project-wide data rules also apply: no nullable columns, no foreign keys or
  cross-table constraints.

## Identity and rebuilds

A step's identity is `(revision, introducing commit)`, where the commit is the
first commit on the first-parent path from the release tag that introduced the
step files. Consequences:

- `git commit --amend`, rebase, or squash changes the hash, so the chain prefix
  shrinks and the active data is rebuilt from the longest matching checkpoint
  or the released baseline.
- The previous active data is moved to `DATA_ROOT.developer-data/quarantine/`
  and is never merged back automatically; ordinary business writes made under
  the old identity stay there for manual recovery.
- Checkpoints for older identities remain on disk but are never reused.

## Folding a step into a release

When the next release is published:

1. Put the final effect into that release's migration under
   `internal/migration/sql/`.
2. Remove the step entries from `registered`.
3. Delete the development scripts.
4. Keep `scripts/.gitkeep` and this README: the directory must stay embeddable
   even when no script is left.

Step 2 is mandatory before tagging: a build that still registers steps refuses
`RELEASE_MIGRATION_ONLY`, so baseline promotion fails loudly instead of running
release migrations through a development build.

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| `development migration N SQL is missing` | SQL declared but not embedded under `scripts/N.sql` |
| `has neither SQL nor a conversion` | Entry added without a payload |
| `SQL path is ..., want ...` | File not named after its revision |
| `must be non-empty and end with a semicolon` | SQL file empty or missing trailing `;` |
| `rewritten after introduction` | A completed step file was edited |
| `running server binary does not match checked-out commit` | Start through the dev launcher so `cmd/devbuild` stamps `HEAD` |
| `uncommitted source changes prevent automatic developer data migration` | Commit the work, then restart; see “Local iteration” |
| `has no trusted release baseline` | Active data exists without a baseline; supply `DEV_BASELINE_SOURCE` |
