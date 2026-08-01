import { useState, useRef, useCallback, useEffect } from 'react';
import { message } from 'antd';
import { useTranslation } from 'react-i18next';
import {
  MESSAGE_STATUS_COMPLETED,
  TEMP_SESSION_ID,
} from '../types';
import type { Message, Session } from '../types';
import { messageApi } from '../services/api';
import { logger } from '../logger';

interface UseMessagesResult {
  messages: Message[];
  setMessages: React.Dispatch<React.SetStateAction<Message[]>>;
  loading: boolean;
  /** Call after temp→real transition to skip re-load and preserve streaming UI. */
  markTempToRealTransition: () => void;
  /**
   * Load messages for the current session.
   * Returns the loaded session ID, or null if nothing was loaded.
   */
  loadMessages: () => Promise<void>;
  /**
   * Send a message. Returns whether SSE should connect, and the session ID.
   */
  handleSend: (
    input: string,
    session: Session,
    currentUserPersonId: number,
  ) => Promise<{
    shouldConnect: boolean;
    sessionId: number;
    messageId: number;
  } | null>;
}

/**
 * Manages chat message state: loading history and sending new messages.
 *
 * Handles race conditions via loadId counter — stale responses
 * from previous session loads are silently ignored.
 */
export function useMessages(
  initialSession: Session | null,
): UseMessagesResult {
  const { t } = useTranslation();
  const [messages, setMessages] = useState<Message[]>([]);
  const [loading, setLoading] = useState(false);

  const loadIdRef = useRef(0);
  const prevSessionIdRef = useRef<number | null>(null);
  const isInitialLoadRef = useRef(true);
  const skipLoadRef = useRef(false);
  const sessionRef = useRef(initialSession);
  sessionRef.current = initialSession;

  const markTempToRealTransition = useCallback(() => {
    skipLoadRef.current = true;
  }, []);

  const loadMessages = useCallback(async () => {
    const s = sessionRef.current;
    if (!s || s.id === TEMP_SESSION_ID) return;

    if (skipLoadRef.current) {
      skipLoadRef.current = false;
      return;
    }

    const loadId = ++loadIdRef.current;
    logger.info('Loading messages for session:', s.id, 'loadId:', loadId);

    setLoading(true);
    try {
      const res = await messageApi.list(s.id);
      if (loadId !== loadIdRef.current) return; // stale
      setMessages(res.data);
    } catch (err) {
      logger.error('Failed to load messages:', err);
    } finally {
      if (loadId === loadIdRef.current) setLoading(false);
    }
  }, []);

  const handleSend = useCallback(
    async (input: string, session: Session, userId: number) => {
      try {
        if (session.id === TEMP_SESSION_ID) {
          const res = await messageApi.createAndSend(
            input,
            session.agent_id,
            input.substring(0, 50),
          );
          const newSessionId = res.data.session_id;

          const userMsg: Message = {
            id: res.data.message_id,
            session_id: newSessionId,
            person_id: userId,
            content: input,
            status: MESSAGE_STATUS_COMPLETED,
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
          };

          setMessages([userMsg]);
          return {
            shouldConnect: true,
            sessionId: newSessionId,
            messageId: userMsg.id,
          };
        }

        const res = await messageApi.send(session.id, input);
        const userMsg: Message = {
          id: res.data.message_id,
          session_id: session.id,
          person_id: userId,
          content: input,
          status: MESSAGE_STATUS_COMPLETED,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        };

        setMessages((prev) => [...prev, userMsg]);
        return {
          shouldConnect: true,
          sessionId: session.id,
          messageId: userMsg.id,
        };
      } catch (err) {
        logger.error('Failed to send message:', err);
        message.error(t('messages.sendFailed'));
        return null;
      }
    },
    [t],
  );

  // Handle session changes: reset state, then auto-load
  useEffect(() => {
    const prevId = prevSessionIdRef.current;
    const currentId = sessionRef.current?.id ?? null;

    // Only skip loading when this is a genuine temp→real transition (user sent
    // a message). A switch from an unsent temp session to an unrelated old
    // session should load messages normally.
    const isTempToReal = prevId === TEMP_SESSION_ID && currentId !== null && currentId !== TEMP_SESSION_ID && skipLoadRef.current;
    if (isTempToReal) {
      prevSessionIdRef.current = currentId;
      skipLoadRef.current = true;
      return;
    }

    loadIdRef.current += 1;
    setMessages([]);
    isInitialLoadRef.current = true;
    prevSessionIdRef.current = currentId;

    // Auto-load after session change (defer to next tick so state resets commit first)
    if (currentId !== null && currentId !== TEMP_SESSION_ID) {
      const loadId = loadIdRef.current;
      setLoading(true);
      messageApi.list(currentId).then(res => {
        if (loadId !== loadIdRef.current) return; // stale
        setMessages(res.data);
      }).catch(err => {
        logger.error('Failed to load messages:', err);
      }).finally(() => {
        if (loadId === loadIdRef.current) setLoading(false);
      });
    }
  }, [initialSession?.id]);

  return {
    messages,
    setMessages,
    loading,
    markTempToRealTransition,
    loadMessages,
    handleSend,
  };
}
