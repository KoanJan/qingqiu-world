# relay-message-to-person

This semantic case checks one behavior: after the user asks the Agent to pass a
message to another named Person, the Agent delivers that message to the correct
recipient's session and carries the request's key information, instead of
replying only in the requesting chat, addressing the wrong Person, or dropping
details.

The [scenario.json](scenario.json) is authoritative for the chat
request, the target person name, the criteria, and the limits.

| Item | Value |
| --- | --- |
| Track | Semantic (`S`) |
| Executor | [`scripts/run.ts`](scripts/run.ts) |
| Definition validator | [`scripts/validate_scenario.ts`](scripts/validate_scenario.ts) |
| Result validator | [`scripts/validate_result.ts`](scripts/validate_result.ts) |
| Case contract | [`scripts/case_contract.ts`](scripts/case_contract.ts) |
| Product entry | `POST /api/chat/new`, `POST /api/agents`, `GET /api/messages/:id`; read-only `SELECT` on `persons`, `sessions`, `participant_sessions`, `messages`, `events`, `decisions`, `actions`, `action_effects` |
| Data | The fixed request and target name in `scenario.json`; no committed source document |
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
configures a user, LLM, embedding, then creates two Agents: the subject Agent
(`Relay assistant`) and the declared target Person named in the scenario. The
target is a second AI Person in this isolated world, so the Agent can only
reach it by resolving the name in its contactable-persons context. The runner
sends the declared request in a new chat session with the subject Agent. It
retries no run; a transport or setup failure is retained as a diagnostic
`ERROR`.

The shared S-case monitor asks the AI test agent whether each new reply ends
the request. While the reply remains open, it checks durable state every 30
seconds; two identical ongoing polls trigger an AI stop review using the new
runtime logs. The scenario timeout bounds capture. Follow the stop-review
handshake in the shared [Skill](../SKILL.md).

## Commands

Run from the repository root:

~~~bash
npm --prefix tests run case -- cases/S/relay-message-to-person/scripts/validate_scenario.ts
npm --prefix tests run case -- cases/S/relay-message-to-person/scripts/run.ts
npm --prefix tests run case -- cases/S/relay-message-to-person/scripts/validate_result.ts --capture <run-dir>
npm --prefix tests run case -- cases/S/relay-message-to-person/scripts/validate_result.ts <run-dir>
~~~

A completed capture is `INCONCLUSIVE`, not `PASS`. Follow the shared
[Skill](../SKILL.md) to judge the captured evidence; record the `judge` object,
each criterion's observation and evidence references, and any optional
`data/calibration.json`. List new evidence in `artifacts`, then run
`validate_result.ts`. A non-author reviewer reruns the committed case locally
for PR acceptance; generated reports remain Git-ignored.

## Expected result

The request is
`请帮我给林小满带句话：明天上午十点在图书馆门口见，记得带上蓝色文件夹。`.
After a completed run:

- `request_preserved`: the user message in the requesting chat is exactly that
  text and is sent by the user.
- `recipient_correct`: a 1v1 session exists whose participants are exactly the
  subject Agent and the declared target person, and at least one message in
  that session was sent by the subject Agent; the backing `chat` action's
  `plan_json` targets either that session or the target person.
- `decision_chain`: that message is linked by an `action_effects` row
  (`effect_type=2`) to a `chat` action (`type=0`) whose decision belongs to the
  subject Agent.
- `content_preserved`: the delivered message conveys the appointment at 10 a.m.
  the next day (or its equivalent resolved date and time), at the library
  entrance, and the reminder to bring the blue folder.

The runner checks the first three mechanically. `content_preserved` and the two
forbidden criteria require semantic judgment over the captured evidence.
Forbidden outcomes are:

- `wrong_recipient`: delivering the relay only to the requesting user or another
  person, targeting a session without the declared target, or leaving only an
  acknowledgment in the requesting session.
- `content_loss`: omitting or changing the time, place, or blue-folder reminder.

The case expects at least one correctly linked delivery in a target session.
Additional messages in that session, including replies from the target Person,
are captured for judgment but are not part of the required delivery record.

## Evidence

Each run directory contains:

- `data/metadata.json`: scenario checksum, provider model
  identifiers, limits, and the executor command.
- `data/capture.json`: runner mechanical facts, written before judgment.
- `data/session.json`: the requesting chat response, requesting-session
  messages, every target-session message snapshot, full pre-stop messages, and
  the stop reason.
- `data/stop_monitor.json`: stop reason, state polls, and AI stop reviews.
- `data/db_evidence.json`: read-only `persons`, `sessions`,
  `participant_sessions`, `messages`, `events`, `decisions`, `actions`, and
  `action_effects` rows.
- `data/build.log` and `data/server.log`: build and owned-process output.
- `data/server_pre_stop.log`: runtime output captured before runner shutdown.

The chain to inspect is:
`messages` (original request) -> `events` (`event_type=1`, `ref_id=message.id`)
as the origin record; separately the target session (`participant_sessions`
containing both Agent and target) -> `messages` (Agent-authored) <-
`action_effects` (`effect_type=2`, `effect_id=message.id`) -> `actions`
(`type=0`, `id=action_id`) -> `decisions` (`id=decision_id`, Agent person).
Also inspect the chat action's `plan_json` for `session_id` /
`recipient_person_id` and its `background` and `reason` for the decision itself.

Mechanical failure prefixes:

- `record`: a required persistence record or link is absent.
- `state`: no 1v1 session contains exactly the Agent and the target, or the chat
  action plan does not target that session or the target person.

## Not proven

This case does not verify delivery UI, read receipts, or notification to the
human user; the target is a second AI Person inside the isolated world. It does
not exercise relay through pre-existing group sessions, multiple recipients,
forwarding files, or recipient replies. It proves recipient routing, decision
provenance, and request-content fidelity on one captured run only. The content
verdict is an operator/agent-session judgment with evidence locations, not an
automated scoring step.

## Result meanings

| Status | Meaning |
| --- | --- |
| `INCONCLUSIVE` | A capture completed and passed mechanical checks; semantic judgment is still required. |
| `PASS` | A supported judgment and the captured evidence satisfy every criterion and mechanical check for this run. |
| `FAIL` | The product completed but a required record, state, recipient, or criterion contradicts the scenario. |
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
| C1: The target-session message conveys the 10 a.m. tomorrow meeting at the library entrance and the blue-folder reminder. | `supported` |
| C2: The target-session message asks the recipient to bring an umbrella. | `contradicted` |
| C3: The Agent delivered the relay through a new 1v1 session created with session_id=-1 and recipient_person_id=7. | `insufficient_evidence` |

The expected labels above are not part of the judge input. Calibration is
optional; when recorded, relevant misclassification calls for another judge or
an `INCONCLUSIVE` result.
