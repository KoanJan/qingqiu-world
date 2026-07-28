export interface Session {
  id: number;
  title: string;
  agent_id: number;
  agent_name: string;
  agent_avatar: string;
  created_at: string;
  updated_at: string | null;
}

export interface Message {
  id: number;
  session_id: number;
  person_id: number;
  content: string;
  status: number;
  created_at: string;
  updated_at: string | null;
}

export interface LLMConfig {
  id: number;
  name: string;
  model_id: string;
  base_url: string;
  api_key: string;
  description: string;
  created_at: string;
  updated_at: string | null;
}

export interface EmbeddingConfig {
  id: number;
  name: string;
  model_id: string;
  base_url: string;
  api_key: string;
  description: string;
  created_at: string;
  updated_at: string | null;
}

export interface Agent {
  id: number;
  name: string;
  bio: string;
  character_settings: string;
  llm_config_id: number;
  avatar: string;
  knowledge_base_ids: number[];
  created_at: string;
  updated_at: string | null;
}

// AgentBrief is the minimal agent representation for the sidebar agent list.
// Returned by GET /api/agents/with-energy.
export interface AgentBrief {
  id: number;
  name: string;
  avatar: string;
  energy: number;
}

/** Status constant for completed messages. */
export const MESSAGE_STATUS_COMPLETED = 1;

/** Temporary session ID used before a real session is created. */
export const TEMP_SESSION_ID = -1;

/** Interaction type constant for user requests. */
export const INTERACTION_TYPE_REQUEST = 1;
/** Interaction type constant for agent responses. */
export const INTERACTION_TYPE_RESPONSE = 2;

export interface SearchConfig {
  id: number;
  provider: string;
  api_key: string;
  description: string;
  is_active: boolean;
  updated_at: string | null;
}

// Knowledge base index type constants (must match backend model.KnowledgeBaseIndexType*)
/** Flat (brute-force) index type. */
export const KB_INDEX_TYPE_FLAT = 0;
/** Switching index type. */
export const KB_INDEX_TYPE_SWITCHING = 1;
/** HNSW (Hierarchical Navigable Small World) index type. */
export const KB_INDEX_TYPE_HNSW = 2;

export interface KnowledgeBase {
  id: number;
  name: string;
  description: string;
  index_type: number; // 0=flat, 1=switching, 2=hnsw
  index_file_path: string;
  document_count: number;
  vector_count: number;
  deleted_count: number;
  created_at: string;
  updated_at: string;
}

// Document status constants (must match backend model.DocumentStatus*)
/** Document pending processing. */
export const DOC_STATUS_PENDING = 0;
/** Document currently being processed. */
export const DOC_STATUS_PROCESSING = 1;
/** Document processed and ready. */
export const DOC_STATUS_READY = 2;
/** Document processing failed. */
export const DOC_STATUS_FAILED = 3;
/** Document has been deleted. */
export const DOC_STATUS_DELETED = 4;

export interface Document {
  id: number;
  knowledge_base_id: number;
  title: string;
  source: string;
  file_path: string;
  file_size: number;
  file_type: string;
  chunk_count: number;
  status: number; // 0=pending, 1=processing, 2=ready, 3=failed, 4=deleted
  error_message: string;
  created_at: string;
  updated_at: string;
}

export interface SearchResult {
  chunk_id: number;
  document_id: number;
  document_title: string;
  content: string;
  score: number;
  knowledge_base_id: number;
}

// Participant status constants (must match backend model.ParticipantStatus*)
/** Participant is idle and waiting. */
export const PARTICIPANT_STATUS_IDLE = 0;
/** Participant is currently working. */
export const PARTICIPANT_STATUS_WORKING = 1;

export interface SessionAgentStatus {
  agent_id: number;
  name: string;
  avatar: string;
  status: number; // 0=idle, 1=working
}

export interface UserProfile {
  id: number;
  name: string;
  bio: string;
  type: number;
  created_at: string;
  updated_at: string;
}

// SystemLLMConfig represents the singleton system-level LLM configuration
// used for host-level operations like skill ingestion.
export interface SystemLLMConfig {
  llm_config_id: number;
  name: string;
  model_id: string;
}

// PublicExperience source type constants (must match backend model.PublicExperienceSource*)
/** Source from file ingestion. */
export const PUBLIC_EXPERIENCE_SOURCE_INGESTION = 1;
/** Source from agent share. */
export const PUBLIC_EXPERIENCE_SOURCE_SHARE = 2;

// PublicExperience status constants (must match backend model.PublicExperienceStatus*)
/** Experience is being generated. */
export const PUBLIC_EXPERIENCE_STATUS_GENERATING = 1;
/** Experience is active and available. */
export const PUBLIC_EXPERIENCE_STATUS_ACTIVE = 2;
/** Experience generation encountered an error. */
export const PUBLIC_EXPERIENCE_STATUS_ERROR = 3;

export interface PublicExperience {
  id: number;
  title: string;
  description: string;
  when_to_use: string;
  guidelines: string;
  pitfalls: string;
  procedure: string;
  source_type: number; // 1=ingestion, 2=share
  source_id: number;    // uploaded_skills.id for ingestion, agent_experiences.id for share
  source_fingerprint: string;
  status: number; // 1=generating, 2=active, 3=error
  created_at: string;
  updated_at: string;
}

export interface UploadedSkill {
  id: number;
  file_name: string;
  title: string;
  raw_content: string;
  created_at: string;
}

export interface ActivityEvent {
  time: string;
  type: string; // "thinking" | "tool_call" | "guidance"
  content: string; // thinking/guidance text; empty for tool_call
  tool?: string;   // only for tool_call
  target?: string; // only for tool_call
  agent_id: number;
}

export interface ReceivedFileEntry {
  name: string;
  path: string;
  local_path?: string;
  size: number;
  is_dir: boolean;
  children: ReceivedFileEntry[];
}

export interface ReceivedDelivery {
  name: string;
  files: ReceivedFileEntry[];
}
