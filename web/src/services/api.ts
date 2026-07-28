import axios from 'axios';
import { logger } from '../logger';
import type { Session, Message, LLMConfig, EmbeddingConfig, Agent, AgentBrief, SearchConfig, KnowledgeBase, Document, SearchResult, SessionAgentStatus, UserProfile, SystemLLMConfig, PublicExperience, UploadedSkill, ActivityEvent, ReceivedDelivery } from '../types';

declare global {
  interface Window {
    electronAPI?: {
      getServerPort: () => Promise<number>;
      getAppVersion: () => Promise<string>;
      isPackaged: () => Promise<boolean>;
      getPlatform: () => Promise<string>;
      openPath: (filePath: string) => Promise<string>;
      onBackendStatus: (callback: (status: string) => void) => () => void;
      onBackendError: (callback: (error: string) => void) => () => void;
    };
  }
}

const DEFAULT_PORT = 8000;
const SERVER_HOST = '127.0.0.1';

let resolvedPort: number | null = null;
let portPromise: Promise<number> | null = null;

function resolvePort(): Promise<number> {
  if (resolvedPort !== null) return Promise.resolve(resolvedPort);
  if (portPromise) return portPromise;

  portPromise = (async () => {
    try {
      const hasApi = !!window.electronAPI;
      logger.debug('[api] electronAPI available:', hasApi);
      const port = await window.electronAPI?.getServerPort();
      logger.debug('[api] got port from IPC:', port);
      if (port && port > 0) {
        resolvedPort = port;
        logger.info('[api] resolved dynamic port:', port);
        return port;
      }
    } catch (err) {
      logger.warn('[api] getServerPort failed (non-Electron env?):', err);
    }
    // Non-Electron (browser/Docker): use relative URLs, let the serving host proxy /api.
    // Dev mode relies on Vite proxy; Docker uses nginx reverse proxy.
    resolvedPort = -1;
    logger.info('[api] using relative API path (non-Electron)');
    return -1;
  })();

  return portPromise;
}

// Build base URL from resolved port (or default if not yet resolved).
function buildBaseUrl(): string {
  const port = resolvedPort ?? DEFAULT_PORT;
  if (port === -1) return '';
  return `http://${SERVER_HOST}:${port}`;
}

// Static base URL for axios instance creation (before port resolution).
const API_BASE_URL = `${buildBaseUrl()}/api`;

/** Returns the dynamic API base URL reflecting the resolved port. */
export function getDynamicApiBaseUrl(): string {
  return `${buildBaseUrl()}/api`;
}

/** Returns the dynamic server base URL reflecting the resolved port. */
export function getDynamicServerBaseUrl(): string {
  return buildBaseUrl();
}

const api = axios.create({
  baseURL: API_BASE_URL,
  headers: {
    'Content-Type': 'application/json',
  },
});

api.interceptors.request.use(async (config) => {
  const port = await resolvePort();
  if (port === -1) {
    // Non-Electron: use relative path, browser resolves to current origin
    config.baseURL = '/api';
  } else {
    config.baseURL = `http://${SERVER_HOST}:${port}/api`;
  }
  return config;
});

// Response envelope matching the backend response package.
interface ApiEnvelope<T = unknown> {
  code: number;
  message: string;
  data?: T;
}

const CODE_SUCCESS = 0;

// Response interceptor: unwrap the backend business-code envelope.
// Frontend code continues to access response.data as the real payload.
// Business errors (code !== 0) are turned into rejected promises so
// existing .catch() handlers work as before.
api.interceptors.response.use(
  (response) => {
    const body = response.data as ApiEnvelope;
    // Non-envelope responses (e.g. SSE streams) pass through unchanged.
    if (typeof body?.code !== 'number') return response;

    if (body.code === CODE_SUCCESS) {
      response.data = body.data;
      return response;
    }

    // Business error — reject with a structure compatible with
    // existing error.response.data.detail access patterns.
    const err = new Error(body.message) as Error & {
      response?: { data: { detail: string; message: string } };
    };
    err.response = { data: { detail: body.message, message: body.message } };
    return Promise.reject(err);
  },
  (error) => Promise.reject(error),
);

export async function initApiClient(): Promise<number> {
  return resolvePort();
}

/** API client for session CRUD operations. */
export const sessionApi = {
  list: () => api.get<Session[]>('/sessions'),
  get: (id: number) => api.get<Session>(`/sessions/${id}`),
  create: (data: Partial<Session>) => api.post<Session>('/sessions', data),
  update: (id: number, data: Partial<Session>) => api.put<Session>(`/sessions/${id}`, data),
  delete: (id: number) => api.delete(`/sessions/${id}`),
  getActivities: (id: number) => api.get<ActivityEvent[]>(`/sessions/${id}/activities`),
  getReceivedDeliveries: (id: number) => api.get<ReceivedDelivery[]>(`/sessions/${id}/received/deliveries`),
  getReceivedFileUrl: (id: number, delivery: string, path: string) =>
    `${getDynamicServerBaseUrl()}/api/sessions/${id}/received/file?delivery=${encodeURIComponent(delivery)}&path=${encodeURIComponent(path)}`,
};

/** API client for message operations. */
export const messageApi = {
  list: (sessionId: number) => api.get<Message[]>(`/messages/${sessionId}`),
  send: (sessionId: number, content: string) =>
      api.post<{trigger_message_id: number}>(`/chat/send/${sessionId}?message=${encodeURIComponent(content)}`),
  createAndSend: (content: string, agentId?: number, title?: string) =>
    api.post<{session_id: number, trigger_message_id: number}>(`/chat/new?message=${encodeURIComponent(content)}${agentId ? `&agent_id=${agentId}` : ''}${title ? `&title=${encodeURIComponent(title)}` : ''}`),
};

/** API client for LLM configuration management. */
export const llmConfigApi = {
  list: () => api.get<LLMConfig[]>('/llm-configs'),
  get: (id: number) => api.get<LLMConfig>(`/llm-configs/${id}`),
  create: (data: Partial<LLMConfig>) => api.post<LLMConfig>('/llm-configs', data),
  update: (id: number, data: Partial<LLMConfig>) => api.put<LLMConfig>(`/llm-configs/${id}`, data),
  delete: (id: number) => api.delete(`/llm-configs/${id}`),
};

/** API client for embedding configuration. */
export const embeddingConfigApi = {
  get: () => api.get<EmbeddingConfig>('/embedding-config'),
  update: (data: Partial<EmbeddingConfig>) => api.put<EmbeddingConfig>('/embedding-config', data),
};

/** API client for agent CRUD operations. */
export const agentApi = {
  list: () => api.get<Agent[]>('/agents'),
  listWithEnergy: () => api.get<AgentBrief[]>('/agents/with-energy'),
  get: (id: number) => api.get<Agent>(`/agents/${id}`),
  create: (data: Partial<Agent>) => api.post<Agent>('/agents', data),
  update: (id: number, data: Partial<Agent>) => api.put<Agent>(`/agents/${id}`, data),
  delete: (id: number) => api.delete(`/agents/${id}`),
};

/** API client for search configuration. */
export const searchConfigApi = {
  get: () => api.get<SearchConfig>('/search-config'),
  update: (data: Partial<SearchConfig>) => api.put<SearchConfig>('/search-config', data),
};

/** API client for user profile management. */
export const userProfileApi = {
  get: () => api.get<UserProfile>('/user-profile'),
  upsert: (data: { name: string; bio?: string }) => api.put<UserProfile>('/user-profile', data),
};

/** API client for person identity retrieval. */
export const personApi = {
  me: () => api.get<{ id: number; name: string; type: number }>('/persons/me'),
};

/** API client for file upload operations. */
export const uploadApi = {
  uploadAvatar: (file: File) => {
    const formData = new FormData();
    formData.append('file', file);
    return api.post<{ filename: string }>('/uploads/avatar', formData, {
      headers: { 'Content-Type': 'multipart/form-data' },
    });
  },
};

/** API client for chat and agent status operations. */
export const chatApi = {
  getSessionAgents: (sessionId: number) =>
    api.get<SessionAgentStatus[]>(`/chat/agents/${sessionId}`),
};

/** Returns the full avatar URL for the given avatar filename. */
export const getAvatarUrl = (avatar: string) => {
  if (!avatar) return '';
  return `${getDynamicServerBaseUrl()}/avatars/${avatar}`;
};

/** API client for application version retrieval. */
export const versionApi = {
  get: () => api.get<{ version: string }>('/version'),
};

/** API client for knowledge base CRUD and search operations. */
export const kbApi = {
  list: () => api.get<KnowledgeBase[]>('/kb'),
  get: (id: number) => api.get<KnowledgeBase>(`/kb/${id}`),
  create: (data: Partial<KnowledgeBase>) => api.post<KnowledgeBase>('/kb', data),
  update: (id: number, data: Partial<KnowledgeBase>) => api.put<KnowledgeBase>(`/kb/${id}`, data),
  delete: (id: number) => api.delete(`/kb/${id}`),
  listDocuments: (kbId: number) => api.get<Document[]>(`/kb/${kbId}/documents`),
  uploadDocument: (kbId: number, file: File, title?: string) => {
    const formData = new FormData();
    formData.append('file', file);
    if (title) formData.append('title', title);
    return api.post<Document>(`/kb/${kbId}/documents`, formData, {
      headers: { 'Content-Type': 'multipart/form-data' },
    });
  },
  getDocument: (kbId: number, docId: number) => api.get<Document>(`/kb/${kbId}/documents/${docId}`),
  deleteDocument: (kbId: number, docId: number) => api.delete(`/kb/${kbId}/documents/${docId}`),
  search: (kbId: number, query: string, topK?: number) =>
    api.post<SearchResult[]>(`/kb/${kbId}/search`, { query, top_k: topK }),
  searchMulti: (kbIds: number[], query: string, topK?: number) =>
    api.post<SearchResult[]>('/kb/search', { kb_ids: kbIds, query, top_k: topK }),
};

/** API client for system-level LLM configuration. */
export const systemLLMConfigApi = {
  get: () => api.get<SystemLLMConfig>('/system-llm-config'),
  update: (data: { llm_config_id: number }) =>
    api.put<SystemLLMConfig>('/system-llm-config', data),
};

/** API client for public experience management. */
export const publicExperienceApi = {
  list: () => api.get<PublicExperience[]>('/public-experiences'),
  get: (id: number) => api.get<PublicExperience>(`/public-experiences/${id}`),
  delete: (id: number) => api.delete(`/public-experiences/${id}`),
  ingest: (data: { file_name: string; raw_content: string }) =>
    api.post<UploadedSkill>('/public-experiences/ingest', data),
  redistill: (id: number) => api.post(`/public-experiences/${id}/redistill`),
};

/** API client for uploaded skill retrieval. */
export const uploadedSkillApi = {
  get: (id: number) => api.get<UploadedSkill>(`/uploaded-skills/${id}`),
};
