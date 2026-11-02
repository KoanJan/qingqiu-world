# Local Data During Issue Development

Data migrations are integrated per release, not per Issue. Several Issues may successively change the migration function for the same upcoming release. A development database that has already run that migration will not rerun it merely because the code changed. Keep one complete data-directory snapshot from the **latest released version** as the shared upgrade starting point for all Issues in the upcoming release.

## Before Starting an Issue

- Before running code for the Issue, prepare and check a fixed baseline. The database version recorded in it must equal the **latest released version**, and its data must still reflect that version. Stop services using the data directory before backing up the entire local `data` directory, including the database and file resources. Here, `data` means the configured data root, not only the database file. Store the backup outside that directory and verify that it can be restored.
- If the current data has already been migrated or written by any Issue in the upcoming release, do not copy it as the fixed baseline. Prepare the baseline from a backup of the latest released version instead. Keep that baseline unchanged throughout development of the upcoming release; do not update it when Issues merge or contributors rebase.
- Restoring the baseline may overwrite test data created during development. Save test material that must be retained separately; do not add it to the fixed baseline.

## After Rebasing

Each time a development branch is rebased onto an updated parent branch, first review code conflicts and semantic changes. Then stop services using the data directory, remove the entire current `data` directory, restore the **same fixed baseline from the latest released version**, and start the updated code to run the relevant checks. Before deleting anything, confirm that the backup still exists and can be restored. Do not merely roll back the database version marker or restore only the database file.

For example, with `baseline -> issue1 -> issue2 -> issue3`, a contributor working on `issue3` must restore data from `baseline` after every rebase, even if migrations from `issue1` and `issue2` have already run locally. The current code then executes the full release migration through `issue3`. Data produced after `issue1` or `issue2` is never a new baseline.

This rule applies only to a contributor's own development environment; it does not change the migration shipped with the release. Issues continue to integrate changes into the upcoming release's migration function. A PR that changes existing migration logic must receive review by another contributor to ensure earlier steps remain present and retain their meaning. Formal upgrade verification also starts from data at the latest released version and runs the complete current release migration.
