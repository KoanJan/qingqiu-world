-- Schema changes for 0.1.6.

CREATE TABLE IF NOT EXISTS `agent_biographies` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `person_id` integer NOT NULL,
  `content` text NOT NULL,
  `created_at` datetime NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_agent_biographies_person_id` ON `agent_biographies`(`person_id`);
