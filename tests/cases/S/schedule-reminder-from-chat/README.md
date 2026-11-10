# schedule-reminder-from-chat

This semantic case checks one behavior: after the user states a clear future time
and reminder content in chat, the Agent creates a scheduled reminder that matches
the request rather than replying only, dropping the reminder, or scheduling a
different time or topic.

The [scenario.json](scenario.json) is authoritative for the chat
request, the expected delay, the tolerance, the criteria, and the limits.

| Item | Value |
| --- | --- |
| Track | Semantic (`S`) |
| Executor | [`scripts/run.ts`](scripts/run.ts) |
| Definition validator | [`scripts/validate_scenario.ts`](scripts/validate_scenario.ts) |
| Result validator | [`scripts/validate_result.ts`](scripts/validate_result.ts) |
| Case contract | [`scripts/case_contract.ts`](scripts/case_contract.ts) |
| Product entry | `POST /api/chat/new`, `GET /api/messages/:id`, `GET /api/sessions/:id/activities`; read-only `SELECT` on `messages`, `events`, `decisions`, `actions`, `action_effects`, `scheduled_events` |
| Data | The fixed request in `scenario.json`; no committed source document |
| Calibration source | [assets/calibration-claims.md](assets/calibration-claims.md) |

## Prerequisites

- A built Go toolchain for `server/`.
- Node.js 20.19+ (20.x) or 22.12+ and the [pinned test dependencies](../../README.md#typescript-environment).
- Six provider variables. Values are never written to evidence:

    ~~~bash
    export QINGQIU_TEST_LLM_BASE_URL=...
    export QINGQIU_TEST_LLM_MODEL_ID=...
    export QINGQIU_TEST_LLM_API_KEY=...
    export QINGQIU_TEST_EMBEDDING_BASE_URL=...
    export QINGQIU_TEST_EMBEDDING_MODEL_ID=...
    export QINGQIU_TEST_EMBEDDING_API_KEY=...
    ~~~

The runner builds and starts one isolated server under the run's `data/product`,
configures a user, LLM, embedding, and a fresh Agent, then sends the declared
request in a new chat session. It retries no run; a transport or setup failure
is retained as a diagnostic `ERROR`.

The executor does not wait for `trigger_at` to arrive. The shared S-case monitor
asks the AI test agent whether a new reply ends the request. Otherwise it checks
durable state every 30 seconds, including events without Agent observations,
Actions, and Works. Two identical ongoing polls trigger an AI stop review that
also examines new runtime logs. The 180-second scenario limit bounds capture.
Follow the stop-review handshake in the shared [Skill](../SKILL.md).

## Commands

Run from the repository root:

~~~bash
npm --prefix tests run case -- cases/S/schedule-reminder-from-chat/scripts/validate_scenario.ts
npm --prefix tests run case -- cases/S/schedule-reminder-from-chat/scripts/run.ts
npm --prefix tests run case -- cases/S/schedule-reminder-from-chat/scripts/validate_result.ts --capture <run-dir>
npm --prefix tests run case -- cases/S/schedule-reminder-from-chat/scripts/validate_result.ts <run-dir>
~~~

A completed capture is `INCONCLUSIVE`, not `PASS`. Follow the shared
[Skill](../SKILL.md) to judge the captured evidence; record the `judge` object,
each criterion's observation and evidence references, and any optional
`data/calibration.json`. List new evidence in `artifacts`, then run
`validate_result.ts`. A supported, fully evidenced run can report `PASS`
without delivery metadata. A non-author reviewer reruns the committed case
locally for PR acceptance; generated reports remain Git-ignored.

## Expected result

The request is `5分钟后提醒我喝水。`. After a completed run:

- `request_preserved`: the user message in the chat session is exactly that text.
- `reminder_record`: a pending `scheduled_events` row owned by the Agent in that
   chat session is linked by an `action_effects` row (type 3) to a
   `create_alarm` action (type 4), whose decision belongs to the Agent.
- `time_matches`: the event's `trigger_at` is within the declared tolerance of
  the message time plus the declared delay (5 minutes).
- `content_matches`: the event's `message` note, and `action_content` when
  present, asks the user to drink water.

The runner checks the first three mechanically. `content_matches` and the two
forbidden criteria require semantic judgment over the captured
evidence. Forbidden outcomes are:

- `wrong_time`: a past, stale, different-day, or otherwise out-of-tolerance
  `trigger_at`.
- `wrong_content`: an unrelated reminder, or no drink-water reminder in either
  the message note or `action_content`.

## Evidence

Each run directory contains:

- `data/metadata.json`: scenario checksum, provider model
  identifiers, limits, and the executor command.
- `data/capture.json`: runner mechanical facts, written before judgment.
- `data/session.json`: chat creation response, messages, and visible activities.
  It includes the full pre-stop message snapshot and stop reason.
- `data/stop_monitor.json`: stop reason, state polls, and AI stop reviews.
- `data/db_evidence.json`: read-only `messages`, `events`, `decisions`, `actions`,
  `action_effects`, and `scheduled_events` rows.
- `data/server_pre_stop.log`: runtime output captured before the runner stops the server.
- `data/build.log` and `data/server.log`: build and owned-process output.

The chain to inspect is:
`messages` (original request) -> `events` (`event_type=1`, `ref_id=message.id`)
as the origin record; separately `scheduled_events` (pending, same Agent and
session) <- `action_effects` (`effect_type=3`, `effect_id=scheduled.id`) ->
`actions` (`type=4`, `id=action_id`) -> `decisions` (`id=decision_id`, Agent
person). Also inspect the action's `plan_json`, `background`, and `reason` for
the decision itself.

Mechanical failure prefixes:

- `record`: a required persistence record or link is absent.
- `state`: the scheduled event belongs to another Agent or session, or is not pending.
- `time`: `trigger_at` is outside the declared tolerance.

## Not proven

This case does not reach the trigger time, does not verify actual delivery of the
reminder, and does not exercise cancellation, recurrence, timezone boundaries, or
multiple requests. It proves creation, provenance, requested time, and
request-content fidelity only. The content verdict is an
operator/agent-session judgment with evidence locations, not an automated
scoring step.

## Result meanings

| Status | Meaning |
| --- | --- |
| `INCONCLUSIVE` | A capture completed and passed mechanical checks; semantic judgment is still required. |
| `PASS` | A supported judgment and the captured evidence satisfy every criterion and mechanical check for this run. |
| `FAIL` | The product completed but a required record, state, time, or criterion contradicts the scenario. |
| `ERROR` | Setup or execution did not reach a completed observation; inspect the logs and kept diagnostic run. |

`observed_model_calls` is recorded as `null`: the chat Decide/Comprehension
calls are not persisted as work interactions, so this case does not derive a
provider-call count. The limits bound per-process cognitive requests, not a
run-wide provider cap.

## Operator-only calibration

Give the judge only the claims and reference record in
[assets/calibration-claims.md](assets/calibration-claims.md), then compare:

| Claim | Expected label |
| --- | --- |
| C1: The reminder asks the user to drink water. | `supported` |
| C2: The reminder asks the user to walk the dog. | `contradicted` |
| C3: The reminder used the direct-message fast path. | `insufficient_evidence` |

The expected labels above are not part of the judge input. Calibration is
optional; when recorded, relevant misclassification calls for another judge or
an `INCONCLUSIVE` result.
