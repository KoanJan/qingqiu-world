# Developer data lifecycle

This package keeps a developer's working data (`DATA_ROOT`) recoverable while
the code advances through committed, unreleased changes. It runs at startup,
before `database.Init()` and before any business service touches data, and it
owns four responsibilities:

1. freeze an immutable **released baseline**;
2. derive **active** data from that baseline and apply committed development
   steps on the way up;
3. keep **checkpoints** so a rebuild can resume from the last verified state;
4. **quarantine** the previous active copy before every atomic switch.

Development revisions never enter `db_versions`. Release migrations remain the
only mechanism that changes a released database version.

Steps themselves are declared in [`unreleased/`](unreleased/README.md); this
document describes the machinery around them.

## Activation

| Variable | Meaning | Set by |
| --- | --- | --- |
| `DEV_DATA_MODE=1` | Enables the lifecycle. Never inferred from `.git`. | `server/start.sh`; `electron/server-manager.ts` (`1` in dev, `0` in packaged builds) |
| `DEV_REPO_ROOT` | Absolute path of the developer checkout. Required once enabled. | Same two launchers; the Electron server child receives it only in dev. |
| `DEV_BASELINE_SOURCE` | Optional released-data backup used for the first baseline. | Developer only; no launcher sets it. Export it before launching. |
| `RELEASE_MIGRATION_ONLY=1` | Internal: runs only release migrations in a staged copy. | `release_build.go`, for the isolated release binary it builds. |
| `main.buildCommit` | Not an environment variable; stamped via `-ldflags -X` and must equal `HEAD`. | `cmd/devbuild`, which both launchers run first. |

Both development launchers run `cmd/devbuild`, then the server. Ordinary app
startup still refuses builds with registered development steps. Isolated
cross-component tests select `TEST_DATA_MODE=1` instead and apply those steps
after release data preparation; they do not enter this developer lifecycle.

### When `DEV_BASELINE_SOURCE` is absent

The variable matters only while no baseline exists; once `baseline/<release>` is
frozen it is never consulted again, so exporting it permanently is harmless but
unnecessary. Without it, bootstrap decides from the active data:

| Active `DATA_ROOT` | Result |
| --- | --- |
| Absent or empty | A baseline is created at `config.AppVersion` (release-matched initializer for `0.1.21`, tagged release binary afterwards), active data is seeded as `dev.0`, and the development chain is applied. No manual input needed. |
| Present and non-empty | Startup stops before any write with `prepare developer data: existing developer data has no trusted release baseline; provide DEV_BASELINE_SOURCE`. The active data is left untouched. |

A partially deleted store behaves like the second row: a missing or damaged
baseline plus non-empty active data stops the flow instead of being rebuilt.
Supply a verified released backup in `DEV_BASELINE_SOURCE`, or move the active
data away if it is expendable, then start again.

## Managed store

`DATA_ROOT` stays the writable active data set. Everything else lives in
`DATA_ROOT + ".developer-data"`, which must stay outside `DATA_ROOT`:

```
<DATA_ROOT>/                          active data, writable
├── .dev-data-version.json            active state (written last, switched atomically)
├── db/database.db                    SQLite (WAL normalized before snapshots)
└── ...                               file roots: aos/, jinshu/, kb/, avatars/, ...

<DATA_ROOT>.developer-data/           managed store
├── .lock                             exclusive flock for the lifecycle process
├── switch.json                       active-switch journal (absent when idle)
├── bootstrap-pending.json            first-baseline journal (absent when idle)
├── baseline/<release>/
│   ├── data/                         complete released data set
│   └── manifest.json                 release identity, digest, id, status
├── checkpoint/<baseline_id>/dev.<x>.<full commit>/
│   ├── data/                         consistent snapshot after that step
│   └── manifest.json                 same baseline id, chain prefix, digest
├── staging/                          transient work copies: baseline-, dev-, active-, checkpoint-, promotion-, new-release-active-, release-source-
└── quarantine/<label>_<reason>_<UTC nanosecond timestamp>/
```

A **data set** contains SQLite and the active file roots under `DATA_ROOT`.
Legacy root variables are read only as sources for a registered file migration;
when one points outside `DATA_ROOT`, the original path must still be available
when that migration runs. `DATA_ROOT` itself must not be a symlink.

Every snapshot normalizes WAL first: `wal_checkpoint(TRUNCATE)` must succeed, no
uncheckpointed frames may remain, and the database passes `PRAGMA quick_check`
before its version is read.

### Staging prefixes

`staging/` only holds work in progress. Nothing in it is ever reused as a
source of truth; a failed run leaves its copy behind for diagnosis (the
temporary release checkout is always removed), and a later run starts from the
active data, a checkpoint, or the baseline instead.

| Prefix | Purpose | Outcome |
| --- | --- | --- |
| `baseline-*` | Build the first baseline from a supplied backup or a fresh release-matched initializer | Whole directory renamed to `baseline/<release>` |
| `dev-*` | Apply missing development steps to a copy of active data | Only `data/` becomes the active set; the empty parent stays |
| `active-*` | Seed a new active set (`dev.0`) from a baseline | Only `data/` becomes the active set; the empty parent stays |
| `checkpoint-*` | Snapshot a completed development step | Whole directory renamed into `checkpoint/<baseline_id>/` |
| `promotion-*` | Upgrade the frozen baseline N with the release R migration | Whole directory renamed to `baseline/<R>` |
| `new-release-active-*` | Seed active data (`dev.0`) from a promoted baseline | Only `data/` becomes the active set; the empty parent stays |
| `release-source-*` | Materialize the release tag's source tree for the isolated release build | Discarded after the build |

## Startup order

The order is deliberate: verify the source, then take ownership of the data set,
then recover and reconcile. Verification is read-only and runs before the store
exists, so a rejected checkout never creates `.developer-data`, its lock, a
baseline, or active data.

1. Open `DEV_REPO_ROOT` with a pure Go Git reader; resolve the trusted tag for
   `config.AppVersion` (`v0.1.21`).
2. Verify the running source: clean worktree, `buildCommit == HEAD`, the release
   commit is a first-parent ancestor of `HEAD`, and every step in the registry
   chain is valid. Any failure stops here, before the store is created.
3. Create the store and take its `.lock` for the process lifetime.
4. `recoverSwitch()` — finish or roll back an interrupted active switch.
5. Find the newest baseline. Each candidate is revalidated: manifest kind,
   release/entry name, `data_roots`, tree digest, and the tag commit.
6. No baseline → bootstrap (below). Baseline older than the app → promote.
   Baseline newer than the app → stop.
7. `prepareActive()` — ensure active data exists and belongs to this baseline.
8. `reconcile()` — compare the target chain with the active state and apply what
   is missing.
9. Keep the store lock held until the process exits.

### Bootstrapping the first baseline

- With `DEV_BASELINE_SOURCE`: copy the backup into staging, inspect its
  `db_versions`, reject a version newer than the app, resolve its release tag,
  and freeze it as `baseline/<version>`.
- Without it: the active data must be absent or empty. `0.1.21` predates
  isolated release execution, so its baseline is created with the
  release-matched initializer (`database.InitializeFreshAt`). Later releases
  build and run the tagged source instead.
- The baseline must not contain `.dev-data-version.json`.
- The manifest is written, `bootstrap-pending.json` records the intent, the
  staged tree is renamed to `baseline/<version>`, active data is seeded as
  `dev.0`, then the journal is removed.

Existing non-empty active data without a trustworthy baseline is never adopted:
the run stops with `existing developer data has no trusted release baseline;
provide DEV_BASELINE_SOURCE`. Matching `db_versions` alone is not evidence.

### Promoting to a new release

When the app version is newer than the baseline:

1. Copy the baseline to `staging/promotion-*`.
2. Materialize the release tag's tree with the pure Go reader and build it with
   `-buildvcs=false`; the tagged source must already support
   `RELEASE_MIGRATION_ONLY`.
3. Run that binary against the staged copy with `RELEASE_MIGRATION_ONLY=1`,
   `DEV_DATA_MODE=0`, `DATA_ROOT=<staged>`, and remapped file roots.
4. Verify the database version equals the new release, write a manifest whose
   `source_baseline` points at the old baseline, and freeze `baseline/<new>`.
5. Switch active data to a fresh `dev.0` derived from the new baseline with
   reason `promote`. Checkpoints from the old baseline id are never reused.

Development code never prepares a release baseline: the release is always
materialized from the tag commit, not from the working tree.

## Identity and reconciliation

A development identity is `(revision, introducing commit)`. The commit comes
from Git history, not from the step declaration, so amending, rebasing, or
renumbering changes it.

- The active state and the target chain are compared on their longest common
  `(revision, commit)` prefix.
- Equal chains → nothing to do.
- Shorter prefix → rebuild from the active copy (when the prefix still matches
  the active chain), else from the newest checkpoint with the same baseline id
  and an exactly matching chain prefix, else from the released baseline.
- Each remaining step runs on the staged copy: write `pending` state, execute
  SQL and conversion in one database transaction, write `complete` state, then
  save a checkpoint.
- The staged copy is validated (complete state, database version equal to the
  baseline release) and switched in atomically.
- The previous active data is quarantined, never merged. Ordinary business
  writes made under the old identity stay in quarantine for manual recovery.
- Checkpoints of stale identities remain on disk for diagnosis but are never
  reused.

## Local debugging and uncommitted changes

The lifecycle only manages data produced by committed code. A dirty worktree —
including untracked files — or a binary that `cmd/devbuild` did not stamp stops
startup before the managed store is created, so a rejected start leaves
`.developer-data` absent or untouched. There is no supported bypass:
`DEV_DATA_MODE=0` panics while the registry is non-empty, because a build that
registers development steps is not a release build.

Local iteration therefore uses one commit as a scratch pad:

1. Commit the work in progress (`git commit -m WIP`).
2. Start through `server/start.sh` or the Electron dev build; both rebuild the
   binary and stamp the new commit.
3. Keep iterating with `git commit --amend`, then restart.

Amending is not just a convenience: the binary must report the checked-out
commit, so an edited worktree cannot start at all. Keeping a single amended
commit also keeps the history that later becomes the release migration clean.

The cost depends on what changed:

| Change since the last start | Effect |
| --- | --- |
| Ordinary code outside the persistent-data paths | Chain unchanged; active data is reused as is. |
| Persistent-data code (`model/`, `migration/sql/`, historical `migration_0_*.go`, `dev/unreleased/`, `config.go`, `aos/`, `jinshu/`, `speech/storage.go`) without a later registered step | Startup stops: such a change must be introduced together with a development step. |
| Lifecycle orchestration (`dev/` outside `unreleased/`) or `migration.go` | Not treated as persistent data: these change how steps run, and a step cannot correct data they would have produced. |
| `scripts/<revision>.sql`, `scripts/<revision>.go`, or the registry | The introducing commit changes, so the chain diverges and the affected steps replay from a checkpoint or the baseline. Amending the commit that introduced an existing step does the same. |
| `git rebase` or `git pull` rewriting step commits | Same as above; old checkpoints stay on disk but are never reused. |

Scratch files belong outside the checkout (`/tmp`, `$HOME`): an untracked file
inside the repository blocks startup exactly like a modified one. `.env` is
ignored and does not count.

Data written by a run the lifecycle did not manage is the developer's
responsibility: a later clean worktree does not certify it as a committed
checkpoint, so restore a verified state before resuming automation.

## State file

`<DATA_ROOT>/.dev-data-version.json` belongs to active data and checkpoints,
never to a baseline:

| Field | Meaning |
| --- | --- |
| `format_version` | State schema version (currently `1`). |
| `base_release`, `baseline_id`, `base_commit` | Baseline this data was derived from. |
| `dev_revision` | Number of applied steps; equals the chain length. |
| `dev_commit` | Commit of the chain tip; empty exactly at `dev.0`. |
| `applied_migrations` | Ordered `{revision, commit}` entries. |
| `pending_revision` | Set only while a step is mid-flight; must be `0` when complete. |
| `status` | `pending` or `complete`. |

Validation rejects a revision that disagrees with the chain length, a `dev.0`
that carries a commit, a tip commit that differs from the last entry, or a
surviving `pending_revision`.

## Manifests

`manifest.json` beside every `data/` tree records `format_version`, `kind`
(`baseline` or `checkpoint`), `release`, `release_tag`, `release_commit`,
`source_baseline`, `database_version`, `data_roots`, the `chain` for
checkpoints, a SHA-256 `digest` over the whole `data/` tree, an `id` derived from
the manifest content, and `status: complete`. Loading revalidates all of it; a
single changed byte invalidates the copy instead of being trusted.

## Checkpoints

`checkpoint/<baseline_id>/dev.<revision>.<full commit>/` holds one snapshot per
completed development step:

- The parent directory is the baseline id, so checkpoints never cross baselines.
  Promoting a baseline invalidates every checkpoint of the old id; those
  directories stay on disk only as evidence.
- `dev.<revision>` is the chain length after the step and the full commit is the
  commit that introduced it, so the directory name matches the manifest chain.
- A checkpoint is written after its step succeeds and before the active switch,
  so a later rebuild resumes at the last complete step instead of replaying from
  the baseline.
- Loading revalidates the manifest and digest. A checkpoint whose identity is no
  longer in the target chain is ignored, never merged into active data.

## Crash recovery

Both destructive steps are journaled so a crash cannot leave an unexplainable
mix:

- `bootstrap-pending.json` — baseline frozen, active data not yet seeded.
  `prepareActive` completes the seed when the journal matches the baseline.
- `switch.json` — staged data and the quarantine target are recorded before the
  two renames. On the next start: a valid active copy simply drops the journal;
  an invalid active copy is moved to `quarantine/failed_<timestamp>`, then valid
  staged data is promoted, or the quarantined previous copy is restored. If
  neither side is usable the run stops instead of guessing.

Staging copies are kept on failure for diagnosis. Retry always restarts from a
complete active copy, a verified checkpoint, or the baseline — never from a
half-applied step.

## Quarantine naming

```
<release>_dev.<x>[.<commit12>]_<reason>_<UTC timestamp>
```

Reasons are `seed`, `upgrade`, and `promote`. When the previous active data has
no readable state it was never managed here: `unmanaged_<reason>_<timestamp>`.
A present but unusable state file yields `invalid-state_<reason>_<timestamp>`,
and interrupted switches use `failed_<timestamp>`. Sorting by name reproduces
chronological order.

## Invariants

- Release chain and development chain never mix: development revisions stay out
  of `db_versions`, and release migrations never run on development active data.
- The baseline is immutable and only replaced by a verified promotion.
- Every snapshot is a stopped, complete data set; never copy only the database.
- Only `status: complete` state and manifests are reusable; everything else is
  treated as evidence, not as truth.
- One lifecycle process per store (`flock`). Normal-mode processes that open
  `DATA_ROOT` themselves are outside this guard.
- Any unexpected condition stops the flow with an error; nothing is relabeled,
  cleared, or silently rebuilt.

## Operations

| Situation | Behavior |
| --- | --- |
| Delete `DATA_ROOT`, keep the store | Active data is reseeded from the baseline and the chain is replayed. Only baseline + committed steps are restored; later business writes are gone. |
| Delete `DATA_ROOT/.dev-data-version.json` only | Startup stops: active data can no longer be trusted. |
| Delete `DATA_ROOT` and the store | Fresh bootstrap if the active data is absent; otherwise the run stops and asks for `DEV_BASELINE_SOURCE`. |
| Damaged baseline manifest or digest | Startup stops; the baseline is never regenerated automatically. |
| Dirty worktree or binary/source mismatch | Startup stops before reconciliation. Commit the work (see “Local debugging and uncommitted changes”) and restart. |

## Verification

```sh
cd server
go test ./internal/migration/dev/...
```

The package tests cover baseline creation, chain advancement, rebase-driven
rebuilds, interrupt recovery, and SQL rollback.
