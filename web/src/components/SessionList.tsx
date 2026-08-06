import React, { useEffect, useState } from 'react';
import { Button, Modal, message, Input } from 'antd';
import { DeleteOutlined, EditOutlined } from '@ant-design/icons';
import { MessageCircle, Users, Eye } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import type { AgentBrief, Session, SessionParticipant } from '../types';
import { agentApi, sessionApi } from '../services/api';
import { getAvatarUrl } from '../services/api';
import { logger } from '../logger';
import { confirmDelete } from '../utils/confirm';
import AgentAvatar from './AgentAvatar';

// viewMode controls which panel is shown inside the sidebar card.
type ViewMode = 'sessions' | 'agents';

interface SessionListProps {
  currentSessionId: number | null;
  onSelectSession: (session: Session | null) => void;
  onCreateSession: (agentId: number) => void;
  embeddingReady?: boolean;
}

// ParticipantAvatars renders the AI participant avatars for a session.
// For single-participant sessions, it renders a normal avatar (same as before).
// For multi-participant sessions (A2A, future group chats), it renders a
// 九宫格 (up to 3×3) grid of mini avatars, capped at 9.
//
// The container size matches the previous single-avatar size (24px) so the
// sidebar row height stays uniform. Inside, CSS grid lays out the avatars:
//   1 participant  → full-size single avatar
//   2–4 participants → 2-column grid
//   5–9 participants → 3-column grid
const MAX_GRID_AVATARS = 9;

const ParticipantAvatars: React.FC<{ participants: SessionParticipant[] }> = ({ participants }) => {
  // Fallback: no participants or single participant → use the legacy single avatar.
  if (participants.length <= 1) {
    const p = participants[0];
    return (
      <AgentAvatar
        avatar={p?.avatar ?? ''}
        size={24}
        iconSize={12}
        borderRadius="6px"
      />
    );
  }

  // Multi-participant: render a 九宫格 grid (max 9).
  const shown = participants.slice(0, MAX_GRID_AVATARS);
  const cols = shown.length <= 4 ? 2 : 3;

  return (
    <div
      style={{
        display: 'grid',
        gridTemplateColumns: `repeat(${cols}, 1fr)`,
        gap: '1px',
        width: 24,
        height: 24,
        borderRadius: '6px',
        overflow: 'hidden',
        flexShrink: 0,
      }}
    >
      {shown.map((p) => {
        const url = getAvatarUrl(p.avatar);
        if (url) {
          return (
            <img
              key={p.person_id}
              src={url}
              alt={p.name}
              style={{ width: '100%', height: '100%', objectFit: 'cover' }}
            />
          );
        }
        // Fallback: colored square with first letter of name.
        return (
          <div
            key={p.person_id}
            style={{
              width: '100%',
              height: '100%',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              fontSize: 6,
              color: '#fff',
              background: 'var(--color-primary)',
              overflow: 'hidden',
            }}
          >
            {p.name?.charAt(0) ?? '?'}
          </div>
        );
      })}
    </div>
  );
};

const SessionList: React.FC<SessionListProps> = ({ currentSessionId, onSelectSession, onCreateSession }) => {
  const { t } = useTranslation();
  const [sessions, setSessions] = useState<Session[]>([]);
  const [agentsBrief, setAgentsBrief] = useState<AgentBrief[]>([]);
  const [loading, setLoading] = useState(false);
  const [hoveredSessionId, setHoveredSessionId] = useState<number | null>(null);
  const [editModalVisible, setEditModalVisible] = useState(false);
  const [editingSession, setEditingSession] = useState<Session | null>(null);
  const [editTitle, setEditTitle] = useState('');
  const [viewMode, setViewMode] = useState<ViewMode>('sessions');

  const loadSessions = async () => {
    setLoading(true);
    try {
      const response = await sessionApi.list();
      setSessions(response.data ?? []);
    } catch (error) {
      logger.error('Failed to load sessions:', error);
      message.error(t('messages.loadFailed'));
    } finally {
      setLoading(false);
    }
  };

  const loadAgentsBrief = async () => {
    setLoading(true);
    try {
      const response = await agentApi.listWithEnergy();
      setAgentsBrief(response.data ?? []);
    } catch (error) {
      logger.error('Failed to load agents with energy:', error);
      message.error(t('messages.loadFailed'));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadSessions();
  }, []);

  // Refresh agent energy data whenever switching to the agents view.
  useEffect(() => {
    if (viewMode === 'agents') {
      loadAgentsBrief();
    }
  }, [viewMode]);

  const handleDeleteSession = async (sessionId: number, e: React.MouseEvent) => {
    e.stopPropagation();
    confirmDelete({
      title: t('session.confirmDeleteTitle'),
      content: t('session.confirmDelete'),
      okText: t('common.delete'),
      cancelText: t('common.cancel'),
      onOk: async () => {
        try {
          await sessionApi.delete(sessionId);
          await loadSessions();
          message.success(t('session.deleteSuccess'));
          if (currentSessionId === sessionId) {
            onSelectSession(null);
          }
        } catch (error) {
          logger.error('Failed to delete session:', error);
          message.error(t('session.deleteFailed'));
        }
      },
    });
  };

  const handleEditSession = (session: Session, e: React.MouseEvent) => {
    e.stopPropagation();
    setEditingSession(session);
    setEditTitle(session.title || '');
    setEditModalVisible(true);
  };

  const handleUpdateTitle = async () => {
    if (!editingSession || !editTitle.trim()) {
      message.error(t('session.titleEmpty'));
      return;
    }

    try {
      await sessionApi.update(editingSession.id, { title: editTitle.trim() });
      await loadSessions();
      message.success(t('session.updateSuccess'));
      setEditModalVisible(false);
      setEditingSession(null);
      setEditTitle('');
    } catch (error) {
      logger.error('Failed to update session title:', error);
      message.error(t('session.updateFailed'));
    }
  };

  // ── Render: Session List ──
  const renderSessionList = () => (
    <div className="sidebar-content">
      {loading ? (
        <div style={{ textAlign: 'center', padding: '20px', color: 'var(--color-text-placeholder)' }}>
          {t('sidebar.loading')}
        </div>
      ) : sessions.length === 0 ? (
        <div style={{ textAlign: 'center', padding: '20px', color: 'var(--color-text-placeholder)' }}>
          {t('sidebar.noSession')}
        </div>
      ) : (
        sessions.map((session) => {
          // Sessions where the user is not a participant are rendered with a
          // muted style and an eye icon — they are viewable but read-only.
          // This is a relationship fact, not a permission restriction.
          const isObserver = session.is_participant === false;
          return (
            <div
              key={session.id}
              className={`session-item ${currentSessionId === session.id ? 'active' : ''}`}
              onClick={() => onSelectSession(session)}
              onMouseEnter={() => setHoveredSessionId(session.id)}
              onMouseLeave={() => setHoveredSessionId(null)}
              style={isObserver ? { opacity: 0.6 } : undefined}
            >
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                <div style={{ display: 'flex', alignItems: 'center', flex: 1, minWidth: 0 }}>
                  <ParticipantAvatars participants={session.participants ?? []} />
                  <span style={{
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                    fontSize: '13px',
                    marginLeft: '10px'
                  }}>
                    {session.title || t('session.untitled')}
                  </span>
                  {isObserver && (
                    <Eye
                      size={12}
                      style={{
                        marginLeft: '6px',
                        color: 'var(--color-text-placeholder)',
                        flexShrink: 0,
                      }}
                    />
                  )}
                </div>
                {hoveredSessionId === session.id && !isObserver && (
                  <div style={{ display: 'flex', alignItems: 'center', gap: '2px' }}>
                    <Button
                      type="text"
                      size="small"
                      icon={<EditOutlined />}
                      onClick={(e) => handleEditSession(session, e)}
                      style={{ color: 'var(--color-text-secondary)', padding: '2px 4px', height: 'auto', minWidth: 'auto' }}
                    />
                    <Button
                      type="text"
                      size="small"
                      danger
                      icon={<DeleteOutlined />}
                      onClick={(e) => handleDeleteSession(session.id, e)}
                      style={{ padding: '2px 4px', height: 'auto', minWidth: 'auto' }}
                    />
                  </div>
                )}
              </div>
            </div>
          );
        })
      )}
    </div>
  );

  // ── Render: Agent List ──
  const renderAgentList = () => (
    <div className="sidebar-content">
      {loading ? (
        <div style={{ textAlign: 'center', padding: '20px', color: 'var(--color-text-placeholder)' }}>
          {t('sidebar.loading')}
        </div>
      ) : agentsBrief.length === 0 ? (
        <div style={{ textAlign: 'center', padding: '20px', color: 'var(--color-text-placeholder)' }}>
          {t('sidebar.noAgent')}
        </div>
      ) : (
        agentsBrief.map((agent) => (
          <div key={agent.id} className="session-item">
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <div style={{ display: 'flex', alignItems: 'center', flex: 1, minWidth: 0 }}>
                <AgentAvatar
                  avatar={agent.avatar}
                  size={32}
                  iconSize={16}
                  borderRadius="8px"
                />
                <div style={{ marginLeft: '10px', overflow: 'hidden' }}>
                  <div style={{
                    fontSize: '13px',
                    fontWeight: 500,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}>
                    {agent.name}
                  </div>
                  <div style={{
                    fontSize: '11px',
                    color: 'var(--color-text-placeholder)',
                  }}>
                    {t('sidebar.energy', { n: agent.energy })}
                  </div>
                </div>
              </div>
              <Button
                type="text"
                icon={<MessageCircle size={16} />}
                onClick={() => onCreateSession(agent.id)}
                title={t('sidebar.newChat')}
                style={{ color: 'var(--color-text-secondary)' }}
              />
            </div>
          </div>
        ))
      )}
    </div>
  );

  return (
    <div className="app-sidebar">
      {/* Header toolbar with view toggle */}
      <div className="sidebar-header">
        <Button
          type="text"
          icon={<MessageCircle size={18} />}
          onClick={() => {
            setViewMode('sessions');
            loadSessions();
          }}
          title={t('sidebar.sessionList')}
          style={{
            color: viewMode === 'sessions'
              ? 'var(--color-primary)'
              : 'var(--color-text-secondary)',
          }}
        />
        <Button
          type="text"
          icon={<Users size={18} />}
          onClick={() => setViewMode('agents')}
          title={t('sidebar.agentList')}
          style={{
            color: viewMode === 'agents'
              ? 'var(--color-primary)'
              : 'var(--color-text-secondary)',
          }}
        />
      </div>

      {viewMode === 'sessions' ? renderSessionList() : renderAgentList()}

      {/* Edit title modal */}
      <Modal
        title={t('session.editTitle')}
        open={editModalVisible}
        onOk={handleUpdateTitle}
        onCancel={() => {
          setEditModalVisible(false);
          setEditingSession(null);
          setEditTitle('');
        }}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
      >
        <Input
          value={editTitle}
          onChange={(e) => setEditTitle(e.target.value)}
          placeholder={t('session.titlePlaceholder')}
          onPressEnter={handleUpdateTitle}
          style={{ marginTop: '16px' }}
        />
      </Modal>
    </div>
  );
};

export default SessionList;
