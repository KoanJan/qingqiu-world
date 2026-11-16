-- Schema changes for 0.1.21.

CREATE TABLE IF NOT EXISTS `cognitive_memories` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `person_id` integer NOT NULL,
  `description` text NOT NULL,
  `embedding` blob NOT NULL,
  `impression_embedding` blob NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `cognitive_memory_versions` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `memory_id` integer NOT NULL,
  `revision` integer NOT NULL,
  `impression` text NOT NULL DEFAULT "",
  `understanding` text NOT NULL,
  `process_id` integer NOT NULL,
  `created_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `cognitive_processes` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `person_id` integer NOT NULL,
  `status` integer NOT NULL,
  `kind` integer NOT NULL DEFAULT 0,
  `outcome` integer NOT NULL DEFAULT 0,
  `memory_id` integer NOT NULL DEFAULT 0,
  `memory_revision` integer NOT NULL DEFAULT 0,
  `failures` integer NOT NULL DEFAULT 0,
  `storage_failures` integer NOT NULL DEFAULT 0,
  `retry_at` integer NOT NULL DEFAULT 0,
  `epoch` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `daily_memories` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `person_id` integer NOT NULL,
  `local_date` text NOT NULL,
  `shard` integer NOT NULL,
  `group_type` integer NOT NULL DEFAULT 0,
  `group_id` integer NOT NULL DEFAULT 0,
  `summary` text NOT NULL,
  `start_at` datetime NOT NULL,
  `end_at` datetime NOT NULL,
  `embedding` blob NOT NULL,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

ALTER TABLE "decisions" ADD COLUMN "thoughts" TEXT NOT NULL DEFAULT "";

CREATE UNIQUE INDEX IF NOT EXISTS `idx_cognitive_memory_versions_process_id` ON `cognitive_memory_versions`(`process_id`);

CREATE INDEX IF NOT EXISTS `idx_cognitive_owner_updated` ON `cognitive_memories`(`person_id`,`updated_at`);

CREATE INDEX IF NOT EXISTS `idx_cognitive_process_owner` ON `cognitive_processes`(`person_id`,`status`);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_cognitive_revision` ON `cognitive_memory_versions`(`memory_id`,`revision`);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_daily_owner_day_shard` ON `daily_memories`(`person_id`,`local_date`,`shard`);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_daily_owner_group` ON `daily_memories`(`person_id`,`local_date`,`group_type`,`group_id`);

CREATE INDEX IF NOT EXISTS `idx_daily_owner_time` ON `daily_memories`(`person_id`,`start_at`);

CREATE INDEX IF NOT EXISTS `idx_decisions_created` ON `decisions`(`created_at`);

CREATE INDEX IF NOT EXISTS `idx_observations_created` ON `agent_observations`(`created_at`);

CREATE INDEX IF NOT EXISTS `idx_observations_person_created` ON `agent_observations`(`person_id`,`created_at`);
