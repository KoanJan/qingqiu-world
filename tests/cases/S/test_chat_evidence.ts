/** Offline provenance checks for the reminder and relay product workflows. */
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { checkDbEvidence as checkReminder } from "./schedule-reminder-from-chat/scripts/execute_test.ts";
import { checkDbEvidence as checkRelay } from "./relay-message-to-person/scripts/execute_test.ts";
import {
  ActionEffectType, ActionType, EventType, ScheduledEventStatus,
} from "../product_model.ts";

const root = path.dirname(fileURLToPath(import.meta.url));
const scenario = (name: string) => JSON.parse(readFileSync(path.join(root, name, "scenario.json"), "utf8"));

test("reminder provenance links the original request to a timely pending alarm", () => {
  const definition = scenario("schedule-reminder-from-chat");
  const session = { user_id: 1, agent_id: 2, chat: { session_id: 3, message_id: 4 },
    messages: [], activities: [], completed: true };
  const evidence = {
    messages: [{ id: 4, session_id: 3, person_id: 1, content: definition.request,
      created_at: "2026-11-05 10:00:00" }],
    events: [{ id: 5, event_type: EventType.MESSAGE, ref_id: 4 }],
    scheduled_events: [{ id: 6, person_id: 2, session_id: 3,
      status: ScheduledEventStatus.PENDING, trigger_at: "2026-11-05 10:05:00", message: "喝水" }],
    action_effects: [{ id: 7, effect_type: ActionEffectType.SCHEDULED_EVENT, effect_id: 6, action_id: 8 }],
    actions: [{ id: 8, type: ActionType.CREATE_ALARM, decision_id: 9,
      plan_json: JSON.stringify({ trigger_at: "2026-11-05 10:05:00", message: "喝水" }) }],
    decisions: [{ id: 9, person_id: 2 }],
  };
  assert.deepEqual(checkReminder(evidence, session, definition), []);
  evidence.scheduled_events[0]!.trigger_at = "2026-11-05 10:20:00";
  assert.match(checkReminder(evidence, session, definition).join("; "), /time: trigger_at/);
});

test("relay provenance requires a target session and a matching chat action plan", () => {
  const definition = scenario("relay-message-to-person");
  const session = { user_id: 1, agent_id: 2, target_person_id: 3,
    chat: { session_id: 4, message_id: 5 }, messages: [], activities: [], completed: true };
  const evidence = {
    messages: [
      { id: 5, session_id: 4, person_id: 1, content: definition.request },
      { id: 6, session_id: 7, person_id: 2, content: "明天上午十点在图书馆门口见，带蓝色文件夹。" },
    ],
    events: [{ id: 8, event_type: EventType.MESSAGE, ref_id: 5 }],
    participant_sessions: [
      { id: 9, session_id: 7, participant_id: 2 },
      { id: 10, session_id: 7, participant_id: 3 },
    ],
    action_effects: [{ id: 11, effect_type: ActionEffectType.MESSAGE, effect_id: 6, action_id: 12 }],
    actions: [{ id: 12, type: ActionType.CHAT, decision_id: 13, plan_json: JSON.stringify({ session_id: 7 }) }],
    decisions: [{ id: 13, person_id: 2 }],
  };
  assert.deepEqual(checkRelay(evidence, session, definition), []);
  evidence.actions[0]!.plan_json = JSON.stringify({ session_id: 99 });
  assert.match(checkRelay(evidence, session, definition).join("; "), /state: chat action plan/);
});
