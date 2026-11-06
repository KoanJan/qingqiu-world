# Deterministic Cross-Component Cases

From the repository root, run every direct case directory under `tests/cases/D/` with:

~~~bash
python3 tests/cases/D/runall.py
~~~

The script runs each `scripts/run.py` in name order, prints each case's output and exit status, and returns nonzero if any case fails, cannot start, or lacks a runner. Each case writes to its own Git-ignored `reports/<run>/` directory; follow its README for prerequisites, individual execution, assertions, and evidence. Current cases: [background-upload-restart](background-upload-restart/README.md) and [user-profile-restart](user-profile-restart/README.md).

Add a D case using the [case directory standard](../../../spec/development/testing/case_directory.md), then update the [case index](../README.md). The aggregate runner discovers it automatically.
