-- Schema changes for 0.1.7.

CREATE TABLE IF NOT EXISTS `jinshus` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `from_person_id` integer NOT NULL,
  `to_person_id` integer NOT NULL,
  `topic` varchar(255) NOT NULL DEFAULT "",
  `description` text NOT NULL DEFAULT "",
  `created_at` datetime NOT NULL
);

CREATE INDEX IF NOT EXISTS `idx_jinshu_from` ON `jinshus`(`from_person_id`);

CREATE INDEX IF NOT EXISTS `idx_jinshu_to` ON `jinshus`(`to_person_id`);

INSERT INTO jinshus (id, from_person_id, to_person_id, topic, description, created_at)
SELECT id, from_person_id, to_person_id, '', remark, created_at
FROM agent_deliveries;

DROP TABLE agent_deliveries;
