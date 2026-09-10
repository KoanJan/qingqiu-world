import { useCallback, useEffect, useRef } from 'react';
import { getDynamicApiBaseUrl } from '../services/api';
import { logger } from '../logger';

export interface UserNotification {
  type: string;
  session_id?: number;
  [key: string]: unknown;
}

interface UseUserSSEOptions {
  enabled: boolean;
  onNotification: (notification: UserNotification) => void;
}

// Maintains the user-level notification stream independently from the
// currently selected session-level message stream.
export function useUserSSE({ enabled, onNotification }: UseUserSSEOptions) {
  const eventSourceRef = useRef<EventSource | null>(null);
  const callbackRef = useRef(onNotification);
  callbackRef.current = onNotification;

  const disconnect = useCallback(() => {
    if (eventSourceRef.current) {
      logger.info('User SSE: disconnecting');
      eventSourceRef.current.close();
      eventSourceRef.current = null;
    }
  }, []);

  useEffect(() => {
    if (!enabled) {
      disconnect();
      return;
    }

    const url = `${getDynamicApiBaseUrl()}/notifications/stream`;
    logger.info('User SSE: connecting', url);
    const eventSource = new EventSource(url);
    eventSourceRef.current = eventSource;

    eventSource.onmessage = (event) => {
      try {
        const notification = JSON.parse(event.data) as UserNotification;
        callbackRef.current(notification);
      } catch (error) {
        logger.error('User SSE: parse error', error, event.data);
      }
    };

    eventSource.onerror = (error) => {
      logger.error('User SSE: connection error (will auto-reconnect)', error);
    };

    return () => {
      eventSource.close();
      if (eventSourceRef.current === eventSource) {
        eventSourceRef.current = null;
      }
    };
  }, [disconnect, enabled]);
}
