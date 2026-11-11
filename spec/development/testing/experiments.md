# Experiments About Mechanism Effects

> **Status: preliminary; to be completed through an actual trial.** X cases may require long-running, repeated research experiments and substantial model time and budget. The principles below are a starting point, not a complete, trial-verified method for designing, running, or judging an X case. Before relying on an X result, define and review the experiment's protocol, execution limits, analysis, and evidence requirements for its specific question, then update this standard based on an actual trial. X is not a routine D/S acceptance or PR gate.

Put a cross-component experiment under `tests/cases/X/<case-name>/` using the [case directory layout](case_directory.md). Use the [test contract](README.md) to define the question and [evidence and reports](evidence.md) to retain comparable runs. The case name describes the mechanism question without an `x-` prefix or ordinal number.

- State a hypothesis, the intervention, comparison condition, observable outcomes, and plausible confounders before running an experiment. Keep initial state, model configuration, tools, and environment comparable where practical; record what could not be held constant.
- Record repeated runs and the behavior that actually occurred, including retrieved evidence, decisions, actions, and observed effects. Programmatic counts and differences should be computed by code. A test agent may analyze their meaning and limitations, but a difference or a recalled item alone does not prove that the proposed mechanism caused the outcome.
- Keep exploratory or longitudinal experiments separate from routine PR checks. Use their findings to revise a design or define a targeted regression scenario; do not convert one noisy run into a universal product rule.

An experiment protocol must name its unit of comparison, intervention, control condition, starting-state equivalence, repeated-run plan, measured variables, and analysis rule before execution. Change only the intended mechanism where possible; document other differences between conditions as potential confounders. Save each run's inputs and traces, compute observed differences, then discuss remaining confounders.
