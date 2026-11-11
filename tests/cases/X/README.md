# Mechanism Experiments

**Status: future work.** No executable X case has been added or validated yet. Mechanism experiments may need long-running, repeated trials and substantial model budget; the current [experiment standard](../../../spec/development/testing/experiments.md) records preliminary principles and remains to be completed through an actual case. X results are not part of routine D/S acceptance or PR checks.

Put each future experiment in its own `tests/cases/X/<case-name>/` directory, write each run under that case's Git-ignored `reports/<run>/`, and follow the [case directory standard](../../../spec/development/testing/case_directory.md) for file placement. Before relying on its result, complete and review the case's protocol, execution limits, analysis, and evidence requirements. Add a runnable case to the [case index](../README.md) when it exists.
