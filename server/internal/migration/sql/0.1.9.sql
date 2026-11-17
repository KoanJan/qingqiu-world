-- Schema changes for 0.1.9.

CREATE TABLE IF NOT EXISTS `kb_access` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `person_id` integer NOT NULL,
  `kb_id` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `ps_digests` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `person_id` integer NOT NULL,
  `digest` text NOT NULL,
  `created_at` datetime NOT NULL
);

CREATE INDEX IF NOT EXISTS `idx_kb_access_kb_id` ON `kb_access`(`kb_id`);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_kb_access_person_kb` ON `kb_access`(`person_id`,`kb_id`);

CREATE INDEX IF NOT EXISTS `idx_ps_digests_person_id` ON `ps_digests`(`person_id`);

-- Ignore malformed JSON, non-array values, non-integer IDs, and stale KB IDs.
INSERT OR IGNORE INTO kb_access (person_id, kb_id, created_at, updated_at)
SELECT ac.person_id, kb.id, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM agent_configs AS ac
JOIN json_each(
  CASE WHEN json_valid(ac.knowledge_base_ids)
    THEN CASE WHEN json_type(ac.knowledge_base_ids) = 'array'
      THEN ac.knowledge_base_ids ELSE '[]' END
    ELSE '[]' END
) AS binding
JOIN knowledge_bases AS kb ON kb.id = binding.value
WHERE binding.type = 'integer';

ALTER TABLE agent_configs DROP COLUMN knowledge_base_ids;
