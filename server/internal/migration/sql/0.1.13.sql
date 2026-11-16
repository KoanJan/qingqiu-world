-- Schema changes for 0.1.13.

CREATE TABLE IF NOT EXISTS `focus_handoffs` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `person_id` integer NOT NULL,
  `session_id` integer NOT NULL,
  `work_id` integer NOT NULL,
  `source` integer NOT NULL,
  `status` integer NOT NULL,
  `orientation` text NOT NULL,
  `summary` text NOT NULL,
  `confirmed_findings` text NOT NULL DEFAULT "",
  `artifact_references` text NOT NULL DEFAULT "",
  `unresolved` text NOT NULL,
  `next_step` text NOT NULL,
  `created_at` datetime NOT NULL
);

ALTER TABLE "works" ADD COLUMN "checkpoint" TEXT NOT NULL DEFAULT "";

ALTER TABLE "works" ADD COLUMN "focus_phase" INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS `idx_focus_handoff_person_session` ON `focus_handoffs`(`person_id`,`session_id`);

CREATE INDEX IF NOT EXISTS `idx_focus_handoffs_created_at` ON `focus_handoffs`(`created_at`);

CREATE INDEX IF NOT EXISTS `idx_focus_handoffs_work_id` ON `focus_handoffs`(`work_id`);
