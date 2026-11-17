-- Development step 1: LLM compatibility probe state.

ALTER TABLE llm_configs ADD COLUMN probe_status INTEGER NOT NULL DEFAULT 0;
ALTER TABLE llm_configs ADD COLUMN probe_config_hash varchar(64) NOT NULL DEFAULT "";
ALTER TABLE llm_configs ADD COLUMN probe_required_tools_ok numeric NOT NULL DEFAULT false;
ALTER TABLE llm_configs ADD COLUMN probe_required_tools_error TEXT NOT NULL DEFAULT "";
ALTER TABLE llm_configs ADD COLUMN probe_structured_output_ok numeric NOT NULL DEFAULT false;
ALTER TABLE llm_configs ADD COLUMN probe_structured_output_error TEXT NOT NULL DEFAULT "";
ALTER TABLE llm_configs ADD COLUMN probe_checked_at INTEGER NOT NULL DEFAULT 0;
