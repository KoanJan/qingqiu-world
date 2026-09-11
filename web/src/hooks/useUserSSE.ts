import { useCallback, useEffect, useRef } from 'react';
import { getDynamicApiBaseUrl } from '../services/api';
import { logger } from '../logger';
import { notifyClientStreamReconnected, publishClientNotification, type ClientNotification } from '../services/clientNotifications';

interface UseUserSSEOptions {
  enabled: boolean;
}

// useUserSSE maintains the application's sole user-scoped realtime stream.
// It owns connection lifetime only; resource-specific listeners subscribe via
// userEvents so switching views never opens another EventSource.
export function useUserSSE({ enabled }: UseUserSSEOptions) {
  const eventSourceRef = useRef<EventSource | null>(null);

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
    let opened = false;

    eventSource.onopen = () => {
      // EventSource calls onopen after automatic reconnect as well. Caches use
      // this synthetic event to revalidate because this version has no replay.
      if (opened) notifyClientStreamReconnected();
      opened = true;
    };

    eventSource.onmessage = (event) => {
      try {
        const notification = JSON.parse(event.data) as ClientNotification;
        // Fan out before the compatibility callback so every feature observes
        // the same parsed envelope from the one physical connection.
        publishClientNotification(notification);
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
