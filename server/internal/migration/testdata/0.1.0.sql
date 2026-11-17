-- SQLite schema from the v0.1.0 tag, generated with that tag's Go models.
-- Fixed test baseline for release upgrade coverage.

CREATE TABLE `agent_configs` (`id` INTEGER PRIMARY KEY AUTOINCREMENT,`person_id` integer NOT NULL DEFAULT 0,`character_settings` text NOT NULL DEFAULT "",`llm_config_id` integer NOT NULL,`knowledge_base_ids` text NOT NULL DEFAULT "[]",`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `agent_deliveries` (`id` INTEGER PRIMARY KEY AUTOINCREMENT,`from_person_id` integer NOT NULL,`to_person_id` integer NOT NULL,`session_id` integer NOT NULL,`paths` text NOT NULL,`remark` text NOT NULL DEFAULT "",`created_at` datetime NOT NULL);

CREATE TABLE `agent_experience_vectors` (`experience_id` integer PRIMARY KEY AUTOINCREMENT,`embedding` blob NOT NULL,`created_at` datetime NOT NULL);

CREATE TABLE `agent_experiences` (`id` integer PRIMARY KEY AUTOINCREMENT,`person_id` integer NOT NULL,`title` varchar(500) NOT NULL,`description` text NOT NULL,`when_to_use` text NOT NULL DEFAULT "",`guidelines` text NOT NULL DEFAULT "",`pitfalls` text NOT NULL DEFAULT "",`procedure` text NOT NULL DEFAULT "",`source` integer NOT NULL DEFAULT 1,`source_id` integer NOT NULL DEFAULT 0,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `agent_narratives` (`id` integer PRIMARY KEY AUTOINCREMENT,`session_id` integer NOT NULL,`person_id` integer NOT NULL,`summary_version` integer NOT NULL,`content` text NOT NULL,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `agent_observations` (`id` integer PRIMARY KEY AUTOINCREMENT,`person_id` integer NOT NULL,`event_id` integer NOT NULL,`importance` real NOT NULL DEFAULT 0.5,`last_accessed_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,`last_scored_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `db_versions` (`id` INTEGER PRIMARY KEY AUTOINCREMENT,`version` varchar(20) NOT NULL,`description` text NOT NULL DEFAULT "",`applied_at` datetime NOT NULL);

CREATE TABLE `document_chunks` (`id` integer PRIMARY KEY AUTOINCREMENT,`knowledge_base_id` integer NOT NULL,`document_id` integer NOT NULL,`vector_id` integer NOT NULL DEFAULT 0,`chunk_index` integer NOT NULL,`content` text NOT NULL,`start_offset` integer NOT NULL DEFAULT 0,`end_offset` integer NOT NULL DEFAULT 0,`deleted` integer NOT NULL DEFAULT 0,`created_at` datetime NOT NULL);

CREATE TABLE `documents` (`id` integer PRIMARY KEY AUTOINCREMENT,`knowledge_base_id` integer NOT NULL,`title` varchar(500) NOT NULL,`source` varchar(500) NOT NULL DEFAULT "",`file_path` varchar(500) NOT NULL DEFAULT "",`file_size` integer NOT NULL DEFAULT 0,`file_type` varchar(20) NOT NULL DEFAULT "",`chunk_count` integer NOT NULL DEFAULT 0,`status` integer NOT NULL DEFAULT 0,`error_message` text NOT NULL DEFAULT "",`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `embedding_configs` (`id` INTEGER PRIMARY KEY AUTOINCREMENT,`name` varchar(100) NOT NULL,`model_id` varchar(100) NOT NULL,`base_url` varchar(255) NOT NULL,`api_key` varchar(255) NOT NULL,`description` text NOT NULL DEFAULT "",`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `entity_profiles` (`id` integer PRIMARY KEY AUTOINCREMENT,`person_id` integer NOT NULL,`entity_type` integer NOT NULL,`entity_id` integer NOT NULL,`narrative` text NOT NULL,`evidence_count` integer NOT NULL DEFAULT 0,`input_md5` text NOT NULL DEFAULT "",`last_updated_at` datetime NOT NULL,`created_at` datetime NOT NULL);

CREATE TABLE `event_vectors` (`event_id` integer PRIMARY KEY AUTOINCREMENT,`embedding` blob NOT NULL,`created_at` datetime NOT NULL);

CREATE TABLE `events` (`id` integer PRIMARY KEY AUTOINCREMENT,`event_type` integer NOT NULL,`ref_id` integer NOT NULL,`created_at` datetime NOT NULL);

CREATE TABLE `interactions` (`id` integer PRIMARY KEY AUTOINCREMENT,`session_id` integer NOT NULL,`work_id` integer NOT NULL,`iteration` integer NOT NULL,`type` integer NOT NULL,`data` text NOT NULL,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `knowledge_bases` (`id` integer PRIMARY KEY AUTOINCREMENT,`name` varchar(255) NOT NULL,`description` text NOT NULL DEFAULT "",`index_type` integer NOT NULL DEFAULT 0,`index_file_path` varchar(500) NOT NULL DEFAULT "",`document_count` integer NOT NULL DEFAULT 0,`vector_count` integer NOT NULL DEFAULT 0,`deleted_count` integer NOT NULL DEFAULT 0,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `llm_configs` (`id` INTEGER PRIMARY KEY AUTOINCREMENT,`name` varchar(100) NOT NULL,`model_id` varchar(100) NOT NULL,`base_url` varchar(255) NOT NULL,`api_key` varchar(255) NOT NULL,`description` text NOT NULL DEFAULT "",`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `message_drafts` (`id` integer PRIMARY KEY AUTOINCREMENT,`person_id` integer NOT NULL,`session_id` integer NOT NULL,`last_read_message_id` integer NOT NULL DEFAULT 0,`content` text NOT NULL DEFAULT "",`status` integer NOT NULL DEFAULT 0,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `messages` (`id` integer PRIMARY KEY AUTOINCREMENT,`session_id` integer NOT NULL,`person_id` integer NOT NULL DEFAULT 0,`content` text NOT NULL,`draft_id` integer,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `model_capabilities` (`id` INTEGER PRIMARY KEY AUTOINCREMENT,`base_url` varchar(500) NOT NULL,`model_id` varchar(100) NOT NULL,`supports_json_schema` integer NOT NULL DEFAULT 0,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `participant_sessions` (`id` integer PRIMARY KEY AUTOINCREMENT,`session_id` integer NOT NULL,`participant_id` integer NOT NULL,`role` integer NOT NULL DEFAULT 2,`status` integer NOT NULL DEFAULT 0,`last_read_message_id` integer NOT NULL DEFAULT 0,`last_active_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `persons` (`id` integer PRIMARY KEY AUTOINCREMENT,`name` varchar(255) NOT NULL,`bio` text NOT NULL DEFAULT "",`avatar` varchar(500) NOT NULL DEFAULT "",`type` integer NOT NULL,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `public_experience_vectors` (`experience_id` integer PRIMARY KEY AUTOINCREMENT,`embedding` blob NOT NULL,`created_at` datetime NOT NULL);

CREATE TABLE `public_experiences` (`id` integer PRIMARY KEY AUTOINCREMENT,`title` varchar(500) NOT NULL,`description` text NOT NULL,`when_to_use` text NOT NULL DEFAULT "",`guidelines` text NOT NULL DEFAULT "",`pitfalls` text NOT NULL DEFAULT "",`procedure` text NOT NULL DEFAULT "",`source_type` integer NOT NULL DEFAULT 1,`source_id` integer NOT NULL DEFAULT 0,`source_fingerprint` varchar(64) NOT NULL DEFAULT "",`status` integer NOT NULL DEFAULT 2,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `scheduled_events` (`id` integer PRIMARY KEY AUTOINCREMENT,`person_id` integer NOT NULL,`session_id` integer NOT NULL,`trigger_message_id` integer NOT NULL DEFAULT 0,`trigger_at` datetime NOT NULL,`message` text NOT NULL,`action` integer NOT NULL DEFAULT 0,`action_content` text NOT NULL DEFAULT "",`status` integer NOT NULL DEFAULT 0,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `search_config` (`id` INTEGER PRIMARY KEY AUTOINCREMENT DEFAULT 1,`provider` varchar(50) NOT NULL DEFAULT "tavily",`api_key` varchar(255) NOT NULL DEFAULT "",`description` text NOT NULL DEFAULT "",`is_active` numeric NOT NULL DEFAULT false,`updated_at` datetime NOT NULL);

CREATE TABLE `sessions` (`id` integer PRIMARY KEY AUTOINCREMENT,`title` varchar(255) NOT NULL DEFAULT "",`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `summaries` (`id` integer PRIMARY KEY AUTOINCREMENT,`session_id` integer NOT NULL,`version` integer NOT NULL,`content` text NOT NULL,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `system_llm_configs` (`id` integer PRIMARY KEY AUTOINCREMENT,`llm_config_id` integer NOT NULL,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE TABLE `uploaded_skills` (`id` integer PRIMARY KEY AUTOINCREMENT,`file_name` varchar(500) NOT NULL DEFAULT "",`title` varchar(500) NOT NULL DEFAULT "",`raw_content` text NOT NULL,`created_at` datetime NOT NULL);

CREATE TABLE `works` (`id` integer PRIMARY KEY AUTOINCREMENT,`person_id` integer NOT NULL,`session_id` integer NOT NULL,`draft_id` integer NOT NULL DEFAULT 0,`type` integer NOT NULL,`description` text NOT NULL,`status` integer NOT NULL DEFAULT 0,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL);

CREATE INDEX `idx_agent_configs_llm_config_id` ON `agent_configs`(`llm_config_id`);

CREATE UNIQUE INDEX `idx_agent_configs_person_id` ON `agent_configs`(`person_id`);

CREATE INDEX `idx_agent_experiences_person_id` ON `agent_experiences`(`person_id`);

CREATE INDEX `idx_agent_experiences_source_id` ON `agent_experiences`(`source_id`);

CREATE INDEX `idx_agent_narratives_person_id` ON `agent_narratives`(`person_id`);

CREATE INDEX `idx_agent_narratives_session_id` ON `agent_narratives`(`session_id`);

CREATE INDEX `idx_agent_status` ON `works`(`status`);

CREATE INDEX `idx_document_chunks_document_id` ON `document_chunks`(`document_id`);

CREATE INDEX `idx_document_chunks_knowledge_base_id` ON `document_chunks`(`knowledge_base_id`);

CREATE INDEX `idx_documents_knowledge_base_id` ON `documents`(`knowledge_base_id`);

CREATE UNIQUE INDEX `idx_entity_profile` ON `entity_profiles`(`person_id`,`entity_type`,`entity_id`);

CREATE INDEX `idx_events_type_created` ON `events`(`event_type`,`created_at`);

CREATE INDEX `idx_from_person` ON `agent_deliveries`(`from_person_id`);

CREATE INDEX `idx_interactions_session_id` ON `interactions`(`session_id`);

CREATE INDEX `idx_interactions_work_id` ON `interactions`(`work_id`);

CREATE INDEX `idx_messages_person_id` ON `messages`(`person_id`);

CREATE INDEX `idx_messages_session_id` ON `messages`(`session_id`);

CREATE UNIQUE INDEX `idx_model_capability_key` ON `model_capabilities`(`base_url`,`model_id`);

CREATE UNIQUE INDEX `idx_observations_person_event` ON `agent_observations`(`person_id`,`event_id`);

CREATE INDEX `idx_participant_sessions_session_id` ON `participant_sessions`(`session_id`);

CREATE INDEX `idx_person_session_status` ON `message_drafts`(`person_id`,`session_id`,`status`);

CREATE INDEX `idx_person_status` ON `works`(`person_id`);

CREATE UNIQUE INDEX `idx_persons_name` ON `persons`(`name`);

CREATE INDEX `idx_public_experiences_source_fingerprint` ON `public_experiences`(`source_fingerprint`);

CREATE INDEX `idx_public_experiences_source_id` ON `public_experiences`(`source_id`);

CREATE INDEX `idx_se_agent_status` ON `scheduled_events`(`status`);

CREATE INDEX `idx_se_person_status` ON `scheduled_events`(`person_id`);

CREATE INDEX `idx_session` ON `agent_deliveries`(`session_id`);

CREATE UNIQUE INDEX `idx_session_version` ON `summaries`(`session_id`,`version`);

CREATE INDEX `idx_to_person` ON `agent_deliveries`(`to_person_id`);

CREATE INDEX `idx_trigger` ON `scheduled_events`(`trigger_at`);
