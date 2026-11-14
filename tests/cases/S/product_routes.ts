/**
 * Product HTTP routes exercised by semantic cases.
 *
 * Path builders keep the versioned API shape in one place so case workflows and
 * subject-entry descriptions cannot drift from each other.
 */
/** Page size that returns a session's full activity list for evidence. */
export const ACTIVITY_PAGE_LIMIT = 200;

export const ChatRoute = {
  CREATE: "/api/chat/new",
  MESSAGES: "/api/messages",
  SESSIONS: "/api/sessions",
  messages: (sessionId: number) => `/api/messages/${sessionId}`,
  activities: (sessionId: number) =>
    `/api/sessions/${sessionId}/activities?limit=${ACTIVITY_PAGE_LIMIT}`,
} as const;

export const AgentRoute = {
  CREATE: "/api/agents",
} as const;

export const KnowledgeBaseRoute = {
  CREATE: "/api/kb",
  documents: (kbId: number) => `/api/kb/${kbId}/documents`,
  document: (kbId: number, documentId: number) => `/api/kb/${kbId}/documents/${documentId}`,
  access: (kbId: number) => `/api/kb/${kbId}/access`,
} as const;

export const SetupRoute = {
  USER_PROFILE: "/api/user-profile",
  LLM_CONFIGS: "/api/llm-configs",
  EMBEDDING_CONFIG: "/api/embedding-config",
} as const;
