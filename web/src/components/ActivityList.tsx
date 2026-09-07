import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Spin } from 'antd';
import { ClipboardList } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { sessionApi } from '../services/api';
import AgentAvatar from './AgentAvatar';
import type { ActivityEvent, SessionAgentStatus } from '../types';
import { logger } from '../logger';

/**
 * Threshold for truncating long activity content.
 */
const CONTENT_TRUNCATE_LENGTH = 200;

/**
 * Maps tool names to display icons. Emoji strings for most tools, lucide icons for special cases.
 */
const toolIcon: Record<string, React.ReactNode> = {
  bash: '>_',
  web_search: '🔍',
  write_notes: '📝',
  scan_my_experience: '🧠',
  recall_my_experience: '🧠',
  read_text_file: '🔍',
  write_text_file: '📝',
  edit_text_file: '📝',
  search_chat_histories: '🔍',
  send_jinshu: '✉️',
  scan_jinshu: '📮',
  read_jinshu: '📖',
  copy_from_jinshu: '📋',
  scan_kb: '📚',
  list_kb_documents: '📑',
};

/**
 * Props for the ActivityList component.
 */
interface ActivityListProps {
  sessionId: number;
  agents: SessionAgentStatus[];
}

/**
 * ActivityList displays the agent's execution timeline for a session.
 *
 * Each event carries its own agent_id, and the component looks up
 * the corresponding agent info from the agents prop — supporting
 * future multi-agent sessions.
 */
const ActivityList: React.FC<ActivityListProps> = ({ sessionId, agents }) => {
  const { t } = useTranslation();
  const [events, setEvents] = useState<ActivityEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [expandedIndices, setExpandedIndices] = useState<Set<number>>(new Set());
  const contentRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const fetchActivities = async () => {
      setLoading(true);
      try {
        const res = await sessionApi.getActivities(sessionId);
        setEvents(res.data);
      } catch (error) {
        logger.error('Failed to load activities, session_id:', sessionId, error);
      } finally {
        setLoading(false);
      }
    };

    fetchActivities();
  }, [sessionId]);

  // Scroll to the latest (bottom) activity record after events are loaded.
  useEffect(() => {
    if (loading || events.length === 0 || !contentRef.current) return;
    contentRef.current.scrollTop = contentRef.current.scrollHeight;
  }, [loading, events.length]);

  const toggleExpand = useCallback((idx: number) => {
    setExpandedIndices(prev => {
      const next = new Set(prev);
      if (next.has(idx)) {
        next.delete(idx);
      } else {
        next.add(idx);
      }
      return next;
    });
  }, []);

  // Build a lookup map from agent_id to agent info.
  const agentMap = useMemo(() => {
    const map: Record<number, SessionAgentStatus> = {};
    for (const agent of agents) {
      map[agent.agent_id] = agent;
    }
    return map;
  }, [agents]);

  if (loading) {
    return (
      <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', height: '100%' }}>
        <Spin size="large" />
      </div>
    );
  }

  if (events.length === 0) {
    return (
      <div className="activity-list">
        <div className="activity-empty">
          <ClipboardList size={20} />
          <span>{t('activity.noRecords')}</span>
        </div>
      </div>
    );
  }

  return (
    <div className="activity-list">
      <div className="activity-content" ref={contentRef}>
        {events.map((event, idx) => {
          const agent = agentMap[event.agent_id];
          const displayName = agent?.name || 'AI';

          if (event.type === 'tool_call') {
            const icon = toolIcon[event.tool || ''] || '🔧';
            const action = t(`activity.tool.${event.tool}`);
            const targetText = event.target || '';
            const needsTruncate = targetText.length > CONTENT_TRUNCATE_LENGTH;
            const expanded = expandedIndices.has(idx);

            return (
              <div key={idx} className="activity-row activity-row-tool_call">
                <div className="activity-row-header">
                  <AgentAvatar avatar={agent?.avatar || ''} size={24} iconSize={12} borderRadius="6px" />
                  <span className="activity-agent-name">{displayName}</span>
                  <span className="activity-time">{event.time}</span>
                </div>
                <div className="activity-summary">
                  {icon} {action}
                  {targetText && (needsTruncate && !expanded ? (
                    <span className="activity-tool-target">
                      {' '}{targetText.slice(0, CONTENT_TRUNCATE_LENGTH)}...
                      <button className="activity-expand-btn" onClick={() => toggleExpand(idx)}>
                        {t('activity.expand')}
                      </button>
                    </span>
                  ) : (
                    <span className="activity-tool-target">
                      {' '}{targetText}
                      {needsTruncate && (
                        <button className="activity-expand-btn" onClick={() => toggleExpand(idx)}>
                          {t('activity.collapse')}
                        </button>
                      )}
                    </span>
                  ))}
                </div>
              </div>
            );
          }

          const fullText = getDisplayText(event);
          const needsTruncate = fullText.length > CONTENT_TRUNCATE_LENGTH;
          const expanded = expandedIndices.has(idx);

          return (
            <div key={idx} className={`activity-row activity-row-${event.type}`}>
              <div className="activity-row-header">
                <AgentAvatar avatar={agent?.avatar || ''} size={24} iconSize={12} borderRadius="6px" />
                <span className="activity-agent-name">{displayName}</span>
                <span className="activity-time">{event.time}</span>
              </div>
              <div className="activity-summary">
                {needsTruncate && !expanded ? (
                  <>
                    {fullText.slice(0, CONTENT_TRUNCATE_LENGTH)}...
                    <button className="activity-expand-btn" onClick={() => toggleExpand(idx)}>
                      {t('activity.expand')}
                    </button>
                  </>
                ) : (
                  <>
                    {fullText}
                    {needsTruncate && (
                      <button className="activity-expand-btn" onClick={() => toggleExpand(idx)}>
                        {t('activity.collapse')}
                      </button>
                    )}
                  </>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
};

/**
 * Returns the full display text for a thinking or guidance event.
 * Tool calls are rendered separately with structured markup.
 */
function getDisplayText(event: ActivityEvent): string {
  switch (event.type) {
    case 'thinking':
      return `🤔 ${event.content}`;
    case 'guidance':
      return event.content ? `🤔 ${event.content}` : '🤔';
    default:
      return event.content || '';
  }
}

export default ActivityList;
