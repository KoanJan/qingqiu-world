-- Schema changes for 0.1.5.

ALTER TABLE works DROP COLUMN type;

CREATE INDEX IF NOT EXISTS idx_agent_status ON works(status);
CREATE INDEX IF NOT EXISTS idx_person_status ON works(person_id);
