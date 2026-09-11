// CLIENT_NOTIFICATION_TYPES is the frontend's single source of client
// notification routes. Components must import these values instead of
// duplicating wire strings.
export const CLIENT_NOTIFICATION_TYPES = {
  CHAT_MESSAGE: 'chat.message',
  CHAT_AGENT_STATUS: 'chat.agent_status',
  CHAT_AGENT_PROCESSING: 'chat.agent_processing',
  NEW_MESSAGE: 'new_message',
  JINSHU_UPDATED: 'jinshu.updated',
  PUBLIC_EXPERIENCE_UPDATED: 'public_experience.updated',
  PUBLIC_EXPERIENCE_DELETED: 'public_experience.deleted',
  STREAM_RECONNECTED: 'system.stream_reconnected',
} as const;

// ClientNotification mirrors the SSE notification envelope. Data remains
// unknown-by-default so each route validates only the fields it consumes.
export interface ClientNotification {
  id?: number;
  type: string;
  resource_id?: number;
  occurred_at?: string;
  data?: Record<string, unknown>;
  session_id?: number;
}

type Listener = (notification: ClientNotification) => void;

// This small in-memory dispatcher decouples the sole EventSource owner (App)
// from mounted and hidden feature views. It is not a persistent event queue.
const listeners = new Set<Listener>();

// publishClientNotification synchronously fans an already-parsed notification
// to interested UI views.
export function publishClientNotification(notification: ClientNotification) {
  listeners.forEach((listener) => listener(notification));
}

// notifyClientStreamReconnected lets caches discard process-local notification
// ID high-water marks, because the server may have restarted while disconnected.
export function notifyClientStreamReconnected() {
  publishClientNotification({ type: CLIENT_NOTIFICATION_TYPES.STREAM_RECONNECTED });
}

// subscribeClientNotifications returns the exact cleanup callback required by useEffect.
export function subscribeClientNotifications(listener: Listener) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
