-- Schema changes for 0.1.10.

ALTER TABLE "knowledge_bases" ADD COLUMN "keyword_ratio" REAL NOT NULL DEFAULT 0.3;
