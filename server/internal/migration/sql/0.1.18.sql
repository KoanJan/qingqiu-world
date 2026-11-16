-- Schema changes for 0.1.18.

CREATE TABLE IF NOT EXISTS `action_effects` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `action_id` integer NOT NULL,
  `effect_type` integer NOT NULL,
  `effect_id` integer NOT NULL,
  `created_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `actions` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `decision_id` integer NOT NULL,
  `type` integer NOT NULL,
  `plan_json` text NOT NULL DEFAULT "",
  `background` text NOT NULL DEFAULT "",
  `reason` text NOT NULL DEFAULT "",
  `status` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `decisions` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `person_id` integer NOT NULL,
  `event_id` integer NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `memory_terms` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `term` text NOT NULL,
  `source_kind` integer NOT NULL,
  `source_id` integer NOT NULL,
  `owner_person_id` integer NOT NULL,
  `source_created_at` datetime NOT NULL,
  `source_version` integer NOT NULL
);

ALTER TABLE "events" ADD COLUMN "payload_json" TEXT NOT NULL DEFAULT "";

CREATE INDEX IF NOT EXISTS `idx_action_effect_target` ON `action_effects`(`effect_type`,`effect_id`);

CREATE INDEX IF NOT EXISTS `idx_action_effects_action_id` ON `action_effects`(`action_id`);

CREATE INDEX IF NOT EXISTS `idx_actions_decision_id` ON `actions`(`decision_id`);

CREATE INDEX IF NOT EXISTS `idx_actions_status` ON `actions`(`status`);

CREATE INDEX IF NOT EXISTS `idx_decisions_person_event` ON `decisions`(`person_id`,`event_id`);

CREATE INDEX IF NOT EXISTS `idx_memory_term_lookup` ON `memory_terms`(`term`,`source_kind`,`owner_person_id`,`source_created_at`);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_memory_term_source` ON `memory_terms`(`term`,`source_kind`,`source_id`);

CREATE UNIQUE INDEX IF NOT EXISTS idx_decisions_external_event ON decisions(person_id, event_id) WHERE event_id > 0;
CREATE INDEX IF NOT EXISTS idx_recall_events_type_ref ON events(event_type, ref_id);
CREATE INDEX IF NOT EXISTS idx_recall_events_self_held_time ON events(ref_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_recall_messages_time ON messages(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_recall_messages_session_time ON messages(session_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_recall_participants_session_person ON participant_sessions(session_id, participant_id);
CREATE INDEX IF NOT EXISTS idx_recall_actions_time ON actions(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_recall_action_effect_source ON action_effects(action_id, effect_type, effect_id);
CREATE INDEX IF NOT EXISTS idx_recall_works_person_time ON works(person_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_recall_handoffs_person_time ON focus_handoffs(person_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_recall_handoffs_work_person_time ON focus_handoffs(work_id, person_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_recall_action_work_target ON actions(
  CASE WHEN json_valid(plan_json) THEN CAST(json_extract(plan_json, '$.target_work_id') AS INTEGER) ELSE 0 END,
  created_at DESC,
  id DESC
) WHERE type IN (2, 3);
