-- Schema changes for 0.1.1.

CREATE TABLE IF NOT EXISTS `agent_states` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `person_id` integer NOT NULL,
  `energy` integer NOT NULL DEFAULT 100,
  `last_recovered_date` text NOT NULL,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_agent_states_person_id` ON `agent_states`(`person_id`);

-- Seed AI persons that existed before AgentState. UTC date is a placeholder;
-- the next energy recovery normalizes it to the configured timezone.
INSERT INTO agent_states (person_id, energy, last_recovered_date, created_at, updated_at)
SELECT p.id, 100, date('now'), CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM persons AS p
WHERE p.type = 1 AND NOT EXISTS (
  SELECT 1 FROM agent_states AS s WHERE s.person_id = p.id
);
