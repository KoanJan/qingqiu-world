-- Schema changes for 0.1.16.

ALTER TABLE "document_chunks" ADD COLUMN "structure_context_json" TEXT NOT NULL DEFAULT "{}";
