-- Schema changes for 0.1.4.

DROP INDEX IF EXISTS "idx_person_session_status";

ALTER TABLE "messages" DROP COLUMN "draft_id";

ALTER TABLE "works" DROP COLUMN "draft_id";

DROP TABLE IF EXISTS "message_drafts";
