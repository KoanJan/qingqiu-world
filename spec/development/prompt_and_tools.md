# Prompt and Tool Standards

The information available to a model in a given turn is determined by the actual input and callable tools. Stored data is not necessarily visible to the model in that turn. Irrelevant fields can also make the input harder to understand.

## Information Selection and Narrative

- Input should serve the current task and identify necessary provenance, time, and state. Resolve deterministic associations and permission boundaries while constructing the input; provide open-ended retrieval when needed. Do not use LLM scores to select information.
- Distinguish source, fact, inference, intent, and completed effect. Do not put information with different epistemic status into one ambiguous field. When evidence is missing, state the query or visibility boundary rather than converting "not found" into "did not happen."
- Separate natural-language narrative from machine fields. Include an ID only when later lookup or verification requires it, and explain what it identifies. Do not present implementation details as task facts for the model.
- Give historical information a clear temporal perspective relative to the current task. Do not present a past motivation as a plan still awaiting execution.
- Describe a template field's positive responsibility and boundary. Avoid steering output with long lists of prohibitions or examples resembling test scenarios. Check model-generated semantics against actual scenarios.

## Tool Contracts

- Keep tool names, descriptions, JSON schemas, parameter validation, and returned fields consistent. State defaults, pagination, and time-range behavior for optional parameters. Do not imply unsupported capabilities through a tool name or generic description.
- A query tool must define what it queries, its permission scope, what it returns, and what it omits. If it returns an index or summary, explain its source and purpose; provide a way to read original content when verification is needed. Keep tool responsibilities consistent with the calling stage.
- Errors should let callers distinguish invalid parameters, insufficient permission, missing source data, and temporary failure. A correctable tool-parameter error may be returned to the model for another call, while also being logged. Never disguise a failure as an empty result.
- Validate model output structure as needed, but do not ask a field to provide proof of facts or scores that the model cannot reliably establish.
