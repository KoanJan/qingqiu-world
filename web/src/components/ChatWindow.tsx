import React, { useEffect, useState, useRef, useCallback, useMemo } from 'react';
import { Input, Button, Spin, message } from 'antd';
import { RobotOutlined } from '@ant-design/icons';
import { Send, Copy, ChevronsUpDown, ChevronsDownUp } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { formatMessageTime } from '../utils/time';
import AgentAvatar from './AgentAvatar';
import AgentStatusBar from './AgentStatusBar';
import ActivityList from './ActivityList';
import ReceivedPanel from './ReceivedPanel';
import { MarkdownRenderer } from 'pd-markdown/web';
import { useSSE } from '../hooks/useSSE';
import { useMessages } from '../hooks/useMessages';
import type { Message, Session, Agent, SessionAgentStatus } from '../types';
import { PARTICIPANT_STATUS_IDLE, PARTICIPANT_STATUS_WORKING, TEMP_SESSION_ID } from '../types';
import { agentApi, chatApi, personApi } from '../services/api';
import { logger } from '../logger';

interface ChatWindowProps {
  session: Session | null;
  onSessionCreated?: (sessionId: number) => void;
}

const ChatWindow: React.FC<ChatWindowProps> = ({ session, onSessionCreated }) => {
  const { t } = useTranslation();
  const [expandedMessages, setExpandedMessages] = useState<Set<number>>(new Set());
  const [inputValue, setInputValue] = useState('');
  const [currentAgent, setCurrentAgent] = useState<Agent | null>(null);
  const [agentStatus, setAgentStatus] = useState<number>(PARTICIPANT_STATUS_IDLE);
  const [sessionAgents, setSessionAgents] = useState<SessionAgentStatus[]>([]);
  const [viewMode, setViewMode] = useState<'chat' | 'activity' | 'received'>('chat');
  const [currentUserPersonId, setCurrentUserPersonId] = useState<number>(0);
  const tabContainerRef = useRef<HTMLDivElement>(null);
  const tabRefs = useRef<Record<string, HTMLButtonElement | null>>({});
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const chatMessagesRef = useRef<HTMLDivElement>(null);
  const isInitialLoadRef = useRef<boolean>(true);

  // Reset initial-load flag and view mode when the session changes.
  useEffect(() => {
    isInitialLoadRef.current = true;
    setViewMode('chat');
  }, [session?.id]);

  const isTempSession = session?.id === TEMP_SESSION_ID;
  const isStreaming = agentStatus !== PARTICIPANT_STATUS_IDLE;

  // agentLookup maps person_id → SessionAgentStatus so each message renders
  // its actual sender's avatar/name. Without this, A2A sessions (where both
  // participants are agents) would show the same agent for every message,
  // because currentAgent only reflects the session's first AI participant.
  const agentLookup = useMemo(() => {
    const m = new Map<number, SessionAgentStatus>();
    for (const a of sessionAgents) m.set(a.agent_id, a);
    return m;
  }, [sessionAgents]);

  // A session is read-only when the user is not a participant — they can view
  // the chat history but cannot send messages. This reflects a relationship
  // fact (the user is not in that conversation), not a permission restriction.
  // Temp sessions are always writable (the user is creating them).
  const isReadOnly = !isTempSession && session?.is_participant === false;

  // ---- Hooks: SSE connection ----

  const { connect: sseConnect, disconnect: sseDisconnect } = useSSE({
    onMessage: (msg: Message) => {
      // person_id now comes from the backend SSE push. Only fall back to
      // currentAgent.id if the backend didn't provide it (backward compat).
      if (!msg.person_id && currentAgent?.id) {
        msg.person_id = currentAgent.id;
      }
      setMessages(prev => [...prev, msg]);
    },
    onAgentStatus: (status: number, agentId?: number) => {
      setAgentStatus(status);
      if (agentId) {
        setSessionAgents(prev =>
          prev.map(a => (a.agent_id === agentId ? { ...a, status } : a)),
        );
      }
    },
  });

  // ---- Hooks: Messages ----

  const {
    messages,
    setMessages,
    loading: messagesLoading,
    markTempToRealTransition,
    handleSend: sendMessage,
  } = useMessages(session);

  // ---- Data loading ----

  // Load session agents
  useEffect(() => {
    if (!session || isTempSession || !session.agent_id) {
      setSessionAgents([]);
      return;
    }

    const load = async () => {
      try {
        const res = await chatApi.getSessionAgents(session.id);
        setSessionAgents(res.data);
      } catch (error) {
        logger.error('Failed to load session agents', error, 'session_id', session.id);
        if (currentAgent) {
          setSessionAgents([
            { agent_id: currentAgent.id, name: currentAgent.name, avatar: currentAgent.avatar, status: PARTICIPANT_STATUS_IDLE },
          ]);
        }
      }
    };
    load();
  }, [session?.id, session?.agent_id]);

  // Load agent
  useEffect(() => {
    if (!session?.agent_id) {
      setCurrentAgent(null);
      return;
    }
    agentApi.list()
      .then(res => {
        const agent = res.data.find(a => a.id === session.agent_id);
        setCurrentAgent(agent || null);
      })
      .catch(err => logger.error('Failed to load agents:', err));
  }, [session?.agent_id]);

  // Load current user
  useEffect(() => {
    personApi.me()
      .then(res => setCurrentUserPersonId(res.data.id))
      .catch(err => logger.error('Failed to load current user person', err));
  }, []);

  // scrollToBottom scrolls the chat messages container to the bottom
  // smooth: if true, uses smooth animation; otherwise jumps directly
  const scrollToBottom = useCallback((smooth: boolean = true) => {
    if (!chatMessagesRef.current || messages.length === 0) return;
    if (smooth) {
      chatMessagesRef.current.scrollTo({
        top: chatMessagesRef.current.scrollHeight,
        behavior: 'smooth',
      });
    } else {
      chatMessagesRef.current.scrollTop = chatMessagesRef.current.scrollHeight;
    }
  }, [messages.length]);

  // Scroll to bottom on new messages
  useEffect(() => {
    if (isInitialLoadRef.current && messages.length > 0) {
      scrollToBottom(false);
      isInitialLoadRef.current = false;
    } else if (messages.length > 0) {
      scrollToBottom(true);
    }
  }, [messages, scrollToBottom]);

  // Cleanup SSE on unmount (useSSE handles this, but explicit for session transitions)
  useEffect(() => {
    return () => sseDisconnect();
  }, []);

  // Connect SSE when a real session is selected — not just when the user
  // sends a message. This ensures read-only A2A sessions (where the user is
  // not a participant) also receive real-time message pushes.
  // Temp sessions (id < 0) are skipped; they connect on first send.
  useEffect(() => {
    if (!session || isTempSession) return;
    sseConnect(session.id);
  }, [session?.id, isTempSession, sseConnect]);

  // Tab indicator position
  useEffect(() => {
    const container = tabContainerRef.current;
    const activeTab = tabRefs.current[viewMode];
    if (!container || !activeTab) return;
    const containerRect = container.getBoundingClientRect();
    const tabRect = activeTab.getBoundingClientRect();
    container.style.setProperty('--indicator-left', `${tabRect.left - containerRect.left}px`);
    container.style.setProperty('--indicator-width', `${tabRect.width}px`);
  }, [viewMode]);

  // Scroll to bottom when switching to chat view — use instant jump since
  // the chat-messages div is freshly mounted and starts at scrollTop=0.
  useEffect(() => {
    if (viewMode !== 'chat') return;
    scrollToBottom(false);
  }, [viewMode, scrollToBottom]);

  // ---- Handlers ----

  const handleSend = async () => {
    if (!inputValue.trim() || !session) return;

    const result = await sendMessage(inputValue, session, currentUserPersonId);
    if (!result) return;

    setInputValue('');
    setAgentStatus(PARTICIPANT_STATUS_WORKING);

    if (result.sessionId !== session.id && onSessionCreated) {
      // Temp→real transition
      markTempToRealTransition();
      onSessionCreated(result.sessionId);
    }

    // Always connect SSE (will be a no-op if already connected to same session)
    sseConnect(result.sessionId);
  };

  const handleCopy = (content: string) => {
    navigator.clipboard.writeText(content).then(() => {
      message.success(t('chat.copied'));
    }).catch(() => {
      // Fallback for environments without clipboard API
      message.error('Copy failed');
    });
  };

  const toggleMessageExpand = useCallback((msgId: number) => {
    setExpandedMessages(prev => {
      const next = new Set(prev);
      if (next.has(msgId)) {
        next.delete(msgId);
      } else {
        next.add(msgId);
      }
      return next;
    });
  }, []);

  const COLLAPSE_THRESHOLD = 500;

  // ---- Render ----

  if (!session) {
    return (
      <div className="empty-state">
        <RobotOutlined className="empty-icon" />
        <div className="empty-text">{t('app.startNewChat')}</div>
        <div className="empty-hint">{t('app.selectOrCreate')}</div>
      </div>
    );
  }

  const isSendDisabled = !inputValue.trim();

  return (
    <>
      <div className="chat-header-row">
        <AgentStatusBar agents={sessionAgents} />
        {!isTempSession && (
          <div className="chat-view-tabs" ref={tabContainerRef}>
            <div className="chat-tab-indicator" />
            <button
              ref={el => { tabRefs.current.chat = el; }}
              className={`chat-tab ${viewMode === 'chat' ? 'active' : ''}`}
              onClick={() => setViewMode('chat')}
            >
              {t('viewTabs.chat')}
            </button>
            <button
              ref={el => { tabRefs.current.activity = el; }}
              className={`chat-tab ${viewMode === 'activity' ? 'active' : ''}`}
              onClick={() => setViewMode('activity')}
            >
              {t('viewTabs.activities')}
            </button>
            <button
              ref={el => { tabRefs.current.received = el; }}
              className={`chat-tab ${viewMode === 'received' ? 'active' : ''}`}
              onClick={() => setViewMode('received')}
            >
              {t('viewTabs.received')}
            </button>
          </div>
        )}
      </div>

      {viewMode === 'activity' ? (
        <ActivityList sessionId={session.id} agents={sessionAgents} />
      ) : viewMode === 'received' ? (
        <ReceivedPanel sessionId={session.id} />
      ) : (
        <>
          <div className="chat-messages" ref={chatMessagesRef}>
            {messagesLoading ? (
              <div style={{ textAlign: 'center', padding: '40px' }}>
                <Spin size="large" />
              </div>
            ) : (
              <>
                {messages.map(msg => {
                  const isMe = msg.person_id === currentUserPersonId;
                  // Resolve the actual sender for non-user messages. In A2A
                  // sessions both sides are agents, so each needs its own
                  // avatar/name from agentLookup. currentAgent is the fallback
                  // for ordinary 1v1 user-agent sessions.
                  const sender = !isMe ? agentLookup.get(msg.person_id) : undefined;
                  const senderAvatar = sender?.avatar ?? currentAgent?.avatar ?? '';
                  const senderName = sender?.name ?? currentAgent?.name ?? 'AI';
                  return (
                  <div key={msg.id} className={`message-item ${isMe ? 'user' : 'assistant'}`}>
                    <div className="message-header">
                      {isMe ? (
                        <>
                          <span className="message-time">{formatMessageTime(new Date(msg.updated_at || msg.created_at))}</span>
                          <span className="message-role">{t('chat.me')}</span>
                        </>
                      ) : (
                        <>
                          <span className="message-role">
                            <AgentAvatar avatar={senderAvatar} size={32} iconSize={16} borderRadius="8px" />
                            {senderName}
                          </span>
                          <span className="message-time">{formatMessageTime(new Date(msg.updated_at || msg.created_at))}</span>
                        </>
                      )}
                    </div>
                    {!isMe && msg.content === '' && isStreaming ? (
                      <div style={{ textAlign: 'center', padding: '8px' }}>
                        <Spin size="small" />
                      </div>
                    ) : (
                      (() => {
                        const isLong = msg.content.length > COLLAPSE_THRESHOLD;
                        const expanded = expandedMessages.has(msg.id);
                        const collapsed = isLong && !expanded;

                        return (
                          <div className={`message-content${collapsed ? ' collapsed' : ''}`}>
                            {!isMe ? (
                              <MarkdownRenderer source={msg.content} />
                            ) : (
                              msg.content
                            )}
                          </div>
                        );
                      })()
                    )}
                    {msg.content && (
                      <div className="message-actions">
                        <button
                          className="copy-btn"
                          onClick={() => handleCopy(msg.content)}
                          title={t('chat.copy')}
                        >
                          <Copy size={14} />
                        </button>
                        {msg.content.length > COLLAPSE_THRESHOLD && (
                          <button
                            className="copy-btn"
                            onClick={() => toggleMessageExpand(msg.id)}
                            title={expandedMessages.has(msg.id) ? t('chat.collapse') : t('chat.expand')}
                          >
                            {expandedMessages.has(msg.id) ? (
                              <ChevronsDownUp size={14} />
                            ) : (
                              <ChevronsUpDown size={14} />
                            )}
                          </button>
                        )}
                      </div>
                    )}
                  </div>
                  );
                })}
                <div ref={messagesEndRef} />
              </>
            )}
          </div>

          {/* Input is only rendered when the user is a participant in this
              session. When isReadOnly is true (user is not in the session),
              the input area is not rendered — this is a relationship fact,
              not a permission restriction. */}
          {!isReadOnly && (
            <div className="chat-input">
              <div className="input-container-wrapper">
                <div className="placeholder-text">{t('app.askAnything')}</div>
                <div className="input-container">
                  <div className="input-area">
                    <Input.TextArea
                      placeholder=""
                      value={inputValue}
                      onChange={e => setInputValue(e.target.value)}
                      onPressEnter={e => {
                        if (!e.shiftKey) {
                          e.preventDefault();
                          handleSend();
                        }
                      }}
                      autoSize={{ minRows: 1, maxRows: 4 }}
                      bordered={false}
                      style={{
                        width: '100%',
                        fontSize: '14px',
                        resize: 'none',
                        backgroundColor: 'transparent',
                      }}
                    />
                  </div>
                  <div className="toolbar-area">
                    <Button
                      type="primary"
                      icon={<Send size={14} />}
                      onClick={handleSend}
                      disabled={isSendDisabled}
                      style={{
                        borderRadius: '50%',
                        width: '28px',
                        height: '28px',
                        padding: 0,
                        backgroundColor: isSendDisabled ? '#d1d5db' : '#1890ff',
                        borderColor: isSendDisabled ? '#d1d5db' : '#1890ff',
                        color: isSendDisabled ? 'var(--color-text-placeholder)' : '#ffffff',
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'center',
                      }}
                    />
                  </div>
                </div>
              </div>
            </div>
          )}
        </>
      )}
    </>
  );
};

export default ChatWindow;
