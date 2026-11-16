-- Schema changes for 0.1.19.

CREATE TABLE IF NOT EXISTS `workspace_uses` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `workspace_id` integer NOT NULL,
  `source_type` integer NOT NULL,
  `source_id` integer NOT NULL,
  `role` integer NOT NULL,
  `created_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `workspaces` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `person_id` integer NOT NULL,
  `relative_path` text NOT NULL,
  `name` text NOT NULL,
  `purpose` text NOT NULL DEFAULT "",
  `created_at` datetime NOT NULL
);

ALTER TABLE "persons" ADD COLUMN "status" INTEGER NOT NULL DEFAULT 1;

ALTER TABLE "sessions" ADD COLUMN "status" INTEGER NOT NULL DEFAULT 1;

CREATE INDEX IF NOT EXISTS `idx_works_session` ON `works`(`session_id`);

CREATE INDEX IF NOT EXISTS `idx_workspace_owner_created` ON `workspaces`(`person_id`,`created_at`);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_workspace_owner_path` ON `workspaces`(`person_id`,`relative_path`);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_workspace_use_source` ON `workspace_uses`(`workspace_id`,`source_type`,`source_id`,`role`);

CREATE INDEX IF NOT EXISTS `idx_workspace_use_workspace` ON `workspace_uses`(`workspace_id`);

DROP INDEX IF EXISTS idx_interactions_session_id;
ALTER TABLE interactions DROP COLUMN session_id;

-- Historical Action type 7 (InspectJinshu) is no longer executable. Shift
-- later values once; old buffered read completions can be discarded.
UPDATE actions
SET type = CASE WHEN type = 7 THEN -1 ELSE type - 1 END
WHERE type BETWEEN 7 AND 12
  AND NOT EXISTS (SELECT 1 FROM db_versions
    WHERE description = '0.1.19 compact retired Jinshu Action and buffered Event types');

UPDATE actions SET status = 2
WHERE type = -1 AND status = 1
  AND NOT EXISTS (SELECT 1 FROM db_versions
    WHERE description = '0.1.19 compact retired Jinshu Action and buffered Event types');

DELETE FROM agent_event_buffers
WHERE event_type = 9
  AND NOT EXISTS (SELECT 1 FROM db_versions
    WHERE description = '0.1.19 compact retired Jinshu Action and buffered Event types');

UPDATE agent_event_buffers SET event_type = event_type - 1
WHERE event_type BETWEEN 10 AND 15
  AND NOT EXISTS (SELECT 1 FROM db_versions
    WHERE description = '0.1.19 compact retired Jinshu Action and buffered Event types');

INSERT INTO db_versions (version, description, applied_at)
SELECT '0.1.19', '0.1.19 compact retired Jinshu Action and buffered Event types', CURRENT_TIMESTAMP
WHERE NOT EXISTS (SELECT 1 FROM db_versions
  WHERE description = '0.1.19 compact retired Jinshu Action and buffered Event types');

-- Keep retired durable Event rows and their IDs, but retire type 7 and shift
-- later event types once.
UPDATE events
SET event_type = CASE WHEN event_type = 7 THEN -1 ELSE event_type - 1 END
WHERE event_type BETWEEN 7 AND 12
  AND NOT EXISTS (SELECT 1 FROM db_versions
    WHERE description = '0.1.19 compact durable Event types and reuse system notifications');

INSERT INTO db_versions (version, description, applied_at)
SELECT '0.1.19', '0.1.19 compact durable Event types and reuse system notifications', CURRENT_TIMESTAMP
WHERE NOT EXISTS (SELECT 1 FROM db_versions
  WHERE description = '0.1.19 compact durable Event types and reuse system notifications');
