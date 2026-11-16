-- Schema changes for 0.1.14.

CREATE TABLE IF NOT EXISTS `content_nodes` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `document_id` integer NOT NULL,
  `revision_id` integer NOT NULL,
  `parent_id` integer NOT NULL DEFAULT 0,
  `ordinal` integer NOT NULL,
  `node_type` integer NOT NULL DEFAULT 0,
  `text` text NOT NULL DEFAULT "",
  `content_hash` varchar(128) NOT NULL DEFAULT "",
  `locator_json` text NOT NULL DEFAULT "{}",
  `metadata_json` text NOT NULL DEFAULT "{}",
  `created_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `document_chunk_nodes` (
  `chunk_id` integer NOT NULL,
  `node_id` integer NOT NULL,
  `ordinal` integer NOT NULL DEFAULT 0,
  PRIMARY KEY (`chunk_id`,`node_id`)
);

CREATE TABLE IF NOT EXISTS `document_revisions` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `document_id` integer NOT NULL,
  `content_hash` varchar(128) NOT NULL,
  `parser_id` varchar(64) NOT NULL DEFAULT "",
  `parser_version` varchar(64) NOT NULL DEFAULT "",
  `normalizer_version` varchar(64) NOT NULL DEFAULT "",
  `canonical_fingerprint` varchar(128) NOT NULL DEFAULT "",
  `blob_path` varchar(500) NOT NULL DEFAULT "",
  `status` integer NOT NULL DEFAULT 0,
  `error_message` text NOT NULL DEFAULT "",
  `provenance_status` integer NOT NULL DEFAULT 0,
  `provenance_error` text NOT NULL DEFAULT "",
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `kb_entities` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `scope_hash` varchar(128) NOT NULL,
  `scope_kb_ids_json` text NOT NULL DEFAULT "[]",
  `normalized_label` varchar(500) NOT NULL,
  `display_label` varchar(500) NOT NULL DEFAULT "",
  `aliases_json` text NOT NULL DEFAULT "[]",
  `state` integer NOT NULL DEFAULT 1,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `kb_relation_evidence` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `relation_id` integer NOT NULL,
  `knowledge_base_id` integer NOT NULL,
  `document_id` integer NOT NULL,
  `revision_id` integer NOT NULL,
  `content_node_id` integer NOT NULL,
  `chunk_id` integer NOT NULL DEFAULT 0,
  `locator_json` text NOT NULL DEFAULT "{}",
  `support_quote` text NOT NULL DEFAULT "",
  `content_hash` varchar(128) NOT NULL DEFAULT "",
  `evidence_fingerprint` varchar(128) NOT NULL DEFAULT "",
  `created_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `kb_relation_jobs` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `job_type` integer NOT NULL DEFAULT 1,
  `state` integer NOT NULL DEFAULT 1,
  `source_work_id` integer NOT NULL DEFAULT 0,
  `relation_id` integer NOT NULL DEFAULT 0,
  `idempotency_key` varchar(128) NOT NULL,
  `payload_json` text NOT NULL DEFAULT "{}",
  `attempts` integer NOT NULL DEFAULT 0,
  `last_error` text NOT NULL DEFAULT "",
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `kb_relations` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `scope_hash` varchar(128) NOT NULL,
  `scope_kb_ids_json` text NOT NULL DEFAULT "[]",
  `subject_entity_id` integer NOT NULL,
  `predicate` varchar(255) NOT NULL DEFAULT "",
  `object_entity_id` integer NOT NULL,
  `state` integer NOT NULL DEFAULT 1,
  `idempotency_key` varchar(128) NOT NULL,
  `evidence_hash` varchar(128) NOT NULL DEFAULT "",
  `policy_version` varchar(64) NOT NULL DEFAULT "",
  `use_count` integer NOT NULL DEFAULT 0,
  `utility_score` integer NOT NULL DEFAULT 0,
  `stale_reason` text NOT NULL DEFAULT "",
  `last_used_at_unix` integer NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS `kb_usage_traces` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `work_id` integer NOT NULL DEFAULT 0,
  `session_id` integer NOT NULL DEFAULT 0,
  `query` text NOT NULL DEFAULT "",
  `reason` text NOT NULL DEFAULT "",
  `authorized_kb_ids_json` text NOT NULL DEFAULT "[]",
  `requested_kb_id` integer NOT NULL DEFAULT 0,
  `document_filter` text NOT NULL DEFAULT "",
  `evidence_handles_json` text NOT NULL DEFAULT "[]",
  `result_status` integer NOT NULL DEFAULT 0,
  `returned_evidence_count` integer NOT NULL DEFAULT 0,
  `returned_document_count` integer NOT NULL DEFAULT 0,
  `evidence_body_truncated` integer NOT NULL DEFAULT 0,
  `query_fingerprint` varchar(128) NOT NULL DEFAULT "",
  `reason_fingerprint` varchar(128) NOT NULL DEFAULT "",
  `created_at` datetime NOT NULL
);

ALTER TABLE "document_chunks" ADD COLUMN "display_text" TEXT NOT NULL DEFAULT "";

ALTER TABLE "document_chunks" ADD COLUMN "input_fingerprint" varchar(128) NOT NULL DEFAULT "";

ALTER TABLE "document_chunks" ADD COLUMN "revision_id" INTEGER NOT NULL DEFAULT 0;

ALTER TABLE "document_chunks" ADD COLUMN "search_text" TEXT NOT NULL DEFAULT "";

ALTER TABLE "document_chunks" ADD COLUMN "token_count" INTEGER NOT NULL DEFAULT 0;

ALTER TABLE "documents" ADD COLUMN "active_revision_id" INTEGER NOT NULL DEFAULT 0;

ALTER TABLE "documents" ADD COLUMN "source_kind" INTEGER NOT NULL DEFAULT 0;

ALTER TABLE "documents" ADD COLUMN "source_uri" varchar(500) NOT NULL DEFAULT "";

ALTER TABLE "knowledge_bases" ADD COLUMN "embedding_dimension" INTEGER NOT NULL DEFAULT 0;

ALTER TABLE "knowledge_bases" ADD COLUMN "embedding_fingerprint" varchar(128) NOT NULL DEFAULT "";

ALTER TABLE "knowledge_bases" ADD COLUMN "embedding_rebuild_error" TEXT NOT NULL DEFAULT "";

ALTER TABLE "knowledge_bases" ADD COLUMN "embedding_rebuild_status" INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS `idx_content_nodes_document_id` ON `content_nodes`(`document_id`);

CREATE INDEX IF NOT EXISTS `idx_content_nodes_parent_ordinal` ON `content_nodes`(`parent_id`,`ordinal`);

CREATE INDEX IF NOT EXISTS `idx_content_nodes_revision_ordinal` ON `content_nodes`(`revision_id`,`ordinal`);

CREATE INDEX IF NOT EXISTS `idx_document_chunk_nodes_node_id` ON `document_chunk_nodes`(`node_id`);

CREATE INDEX IF NOT EXISTS `idx_document_chunks_revision_id` ON `document_chunks`(`revision_id`);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_document_revision_fingerprint` ON `document_revisions`(`canonical_fingerprint`);

CREATE INDEX IF NOT EXISTS `idx_document_revisions_document_id` ON `document_revisions`(`document_id`);

CREATE INDEX IF NOT EXISTS `idx_document_revisions_provenance_status` ON `document_revisions`(`provenance_status`);

CREATE INDEX IF NOT EXISTS `idx_document_revisions_status` ON `document_revisions`(`status`);

CREATE INDEX IF NOT EXISTS `idx_documents_active_revision_id` ON `documents`(`active_revision_id`);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_kb_entities_scope_label` ON `kb_entities`(`scope_hash`,`normalized_label`);

CREATE INDEX IF NOT EXISTS `idx_kb_entities_state` ON `kb_entities`(`state`);

CREATE INDEX IF NOT EXISTS `idx_kb_relation_evidence_chunk_id` ON `kb_relation_evidence`(`chunk_id`);

CREATE INDEX IF NOT EXISTS `idx_kb_relation_evidence_content_node_id` ON `kb_relation_evidence`(`content_node_id`);

CREATE INDEX IF NOT EXISTS `idx_kb_relation_evidence_document_id` ON `kb_relation_evidence`(`document_id`);

CREATE INDEX IF NOT EXISTS `idx_kb_relation_evidence_knowledge_base_id` ON `kb_relation_evidence`(`knowledge_base_id`);

CREATE INDEX IF NOT EXISTS `idx_kb_relation_evidence_relation_id` ON `kb_relation_evidence`(`relation_id`);

CREATE INDEX IF NOT EXISTS `idx_kb_relation_evidence_revision_id` ON `kb_relation_evidence`(`revision_id`);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_kb_relation_evidence_unique` ON `kb_relation_evidence`(`relation_id`,`evidence_fingerprint`);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_kb_relation_jobs_idempotency_key` ON `kb_relation_jobs`(`idempotency_key`);

CREATE INDEX IF NOT EXISTS `idx_kb_relation_jobs_job_type` ON `kb_relation_jobs`(`job_type`);

CREATE INDEX IF NOT EXISTS `idx_kb_relation_jobs_relation_id` ON `kb_relation_jobs`(`relation_id`);

CREATE INDEX IF NOT EXISTS `idx_kb_relation_jobs_source_work_id` ON `kb_relation_jobs`(`source_work_id`);

CREATE INDEX IF NOT EXISTS `idx_kb_relation_jobs_state` ON `kb_relation_jobs`(`state`);

CREATE UNIQUE INDEX IF NOT EXISTS `idx_kb_relations_idempotency_key` ON `kb_relations`(`idempotency_key`);

CREATE INDEX IF NOT EXISTS `idx_kb_relations_object_entity_id` ON `kb_relations`(`object_entity_id`);

CREATE INDEX IF NOT EXISTS `idx_kb_relations_scope_hash` ON `kb_relations`(`scope_hash`);

CREATE INDEX IF NOT EXISTS `idx_kb_relations_state` ON `kb_relations`(`state`);

CREATE INDEX IF NOT EXISTS `idx_kb_relations_subject_entity_id` ON `kb_relations`(`subject_entity_id`);

CREATE INDEX IF NOT EXISTS `idx_kb_usage_traces_session_id` ON `kb_usage_traces`(`session_id`);

CREATE INDEX IF NOT EXISTS `idx_kb_usage_traces_work_id` ON `kb_usage_traces`(`work_id`);
