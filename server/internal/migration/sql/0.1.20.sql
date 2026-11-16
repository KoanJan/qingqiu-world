-- Schema changes for 0.1.20.

CREATE TABLE IF NOT EXISTS `memos` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `person_id` integer NOT NULL,
  `content` text NOT NULL,
  `status` integer NOT NULL,
  `pinned` numeric NOT NULL DEFAULT false,
  `revision` integer NOT NULL,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

CREATE INDEX IF NOT EXISTS `idx_memos_owner_status_pinned_updated` ON `memos`(`person_id`,`status`,`pinned`,`updated_at`);
