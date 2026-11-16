-- Schema changes for 0.1.2.

CREATE TABLE IF NOT EXISTS `agent_event_buffers` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `person_id` integer NOT NULL,
  `event_type` integer NOT NULL,
  `session_id` integer NOT NULL,
  `event_id` integer NOT NULL,
  `payload_json` text NOT NULL,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

ALTER TABLE "agent_states" ADD COLUMN "sleep_since" TEXT NOT NULL DEFAULT "";

CREATE INDEX IF NOT EXISTS `idx_agent_event_buffer_person_event` ON `agent_event_buffers`(`person_id`,`event_id`);

ALTER TABLE scheduled_events DROP COLUMN trigger_message_id;

CREATE INDEX IF NOT EXISTS idx_se_agent_status ON scheduled_events(status);
CREATE INDEX IF NOT EXISTS idx_se_person_status ON scheduled_events(person_id);
CREATE INDEX IF NOT EXISTS idx_trigger ON scheduled_events(trigger_at);

UPDATE agent_states SET sleep_since = '' WHERE sleep_since IS NULL;
