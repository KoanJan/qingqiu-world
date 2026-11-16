-- Schema changes for 0.1.17.

CREATE TABLE IF NOT EXISTS `agent_voices` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `person_id` integer NOT NULL,
  `tts_renderer_id` integer NOT NULL DEFAULT 0,
  `renderer_voice_ref` text NOT NULL DEFAULT "",
  `sample_audio_sha256` varchar(64) NOT NULL DEFAULT "",
  `sample_transcript` text NOT NULL DEFAULT "",
  `sample_audio_extension` varchar(16) NOT NULL DEFAULT "",
  `sample_audio_relative_path` varchar(500) NOT NULL DEFAULT "",
  `sample_locale` varchar(64) NOT NULL DEFAULT "",
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `speech_render_histories` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `message_id` integer NOT NULL,
  `agent_voice_id` integer NOT NULL DEFAULT 0,
  `adapter_request_snapshot` text NOT NULL DEFAULT "",
  `status` integer NOT NULL DEFAULT 0,
  `relative_path` text NOT NULL DEFAULT "",
  `mime_type` varchar(128) NOT NULL DEFAULT "",
  `attempt_count` integer NOT NULL DEFAULT 0,
  `error_message` text NOT NULL DEFAULT "",
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `tts_providers` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `provider` integer NOT NULL,
  `name` varchar(100) NOT NULL,
  `connection_config_json_schema` text NOT NULL,
  `terms_url` text NOT NULL DEFAULT "",
  `description` text NOT NULL DEFAULT "",
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `tts_renderers` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `name` varchar(100) NOT NULL,
  `provider` integer NOT NULL,
  `connection_config_json` text NOT NULL,
  `description` text NOT NULL DEFAULT "",
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

ALTER TABLE "messages" ADD COLUMN "expression_instruction" TEXT NOT NULL DEFAULT "";

ALTER TABLE "scheduled_events" ADD COLUMN "expression_instruction" TEXT NOT NULL DEFAULT "";

CREATE INDEX IF NOT EXISTS `idx_agent_voice_latest` ON `agent_voices`(`person_id`,`id`);

CREATE INDEX IF NOT EXISTS `idx_speech_render_histories_agent_voice_id` ON `speech_render_histories`(`agent_voice_id`);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_speech_render_histories_message_id` ON `speech_render_histories`(`message_id`);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_tts_providers_provider` ON `tts_providers`(`provider`);

CREATE INDEX IF NOT EXISTS `idx_tts_renderers_provider` ON `tts_renderers`(`provider`);

UPDATE messages SET expression_instruction = '' WHERE expression_instruction IS NULL;
