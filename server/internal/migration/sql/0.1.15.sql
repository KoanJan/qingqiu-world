-- Schema changes for 0.1.15.

ALTER TABLE "kb_relations" ADD COLUMN "applicability_note" TEXT NOT NULL DEFAULT "";

UPDATE kb_relations SET applicability_note = '' WHERE applicability_note IS NULL;

ALTER TABLE kb_entities DROP COLUMN aliases_json;
ALTER TABLE kb_relations DROP COLUMN utility_score;
