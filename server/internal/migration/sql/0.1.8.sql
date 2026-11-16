-- Schema changes for 0.1.8.

ALTER TABLE "jinshus" ADD COLUMN "is_read" numeric NOT NULL DEFAULT false;
