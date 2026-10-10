import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Spin } from 'antd';
import { ClipboardList } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { activityApi } from '../services/api';
import type { ActivityEvent } from '../types';
import { logger } from '../logger';

/**
 * Threshold for truncating long activity content.
 */
const CONTENT_TRUNCATE_LENGTH = 200;
const ACTIVITY_REFRESH_INTERVAL_MS = 10_000;

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
  use_workspace: '↗',
  scan_kb: '📚',
  read_kb_evidence: '📖',
  list_kb_documents: '📑',
};

/**
 * Props for the ActivityList component.
 */
interface ActivityListProps {
  workId: number;
  agentId: number;
}

/**
 * ActivityList displays one Focus Work's interaction timeline.
 */
const ActivityList: React.FC<ActivityListProps> = ({ workId, agentId }) => {
  const { t } = useTranslation();
  const [events, setEvents] = useState<ActivityEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [hasMore, setHasMore] = useState(false);
  const [nextBeforeInteractionId, setNextBeforeInteractionId] = useState<number>();
  const [expandedEventIds, setExpandedEventIds] = useState<Set<string>>(new Set());
  const contentRef = useRef<HTMLDivElement>(null);
  const scrollToLatestRef = useRef(true);
  const newestInteractionIdRef = useRef(0);
  const latestLoadRequestRef = useRef(0);

  const loadLatestActivities = useCallback(async () => {
    const requestID = ++latestLoadRequestRef.current;
    setLoading(true);
    setLoadError(false);
    setExpandedEventIds(new Set());
    scrollToLatestRef.current = true;
    newestInteractionIdRef.current = 0;
    try {
      const res = await activityApi.getWorkActivities(agentId, workId);
      if (requestID !== latestLoadRequestRef.current) return;
      setEvents(res.data.events);
      setHasMore(res.data.has_more);
      setNextBeforeInteractionId(res.data.next_before_interaction_id);
      newestInteractionIdRef.current = res.data.next_after_interaction_id ?? 0;
    } catch (error) {
      if (requestID !== latestLoadRequestRef.current) return;
      logger.error('Failed to load activities, work_id:', workId, error);
      setEvents([]);
      setHasMore(false);
      setNextBeforeInteractionId(undefined);
      setLoadError(true);
    } finally {
      if (requestID === latestLoadRequestRef.current) setLoading(false);
    }
  }, [agentId, workId]);

  useEffect(() => {
    const requestTracker = latestLoadRequestRef;
    void loadLatestActivities();
    return () => { requestTracker.current++; };
  }, [loadLatestActivities]);

  // Activity rows are persisted asynchronously while Focus runs. Read only
  // newer interactions on each refresh, and keep any older pages already open.
  useEffect(() => {
    if (loading) return;
    let disposed = false;
    let refreshing = false;
    const refresh = async () => {
      if (refreshing) return;
      refreshing = true;
      try {
        let cursor = newestInteractionIdRef.current;
        do {
          const res = await activityApi.getWorkActivities(agentId, workId, undefined, cursor || undefined);
          if (disposed) return;
          const page = res.data;
          setLoadError(false);
          const content = contentRef.current;
          if (content && content.scrollHeight - content.scrollTop - content.clientHeight <= 40) {
            scrollToLatestRef.current = true;
          }
          if (page.events.length > 0) {
            setEvents(previous => {
              const existingIDs = new Set(previous.map(event => event.id));
              const additions = page.events.filter(event => !existingIDs.has(event.id));
              return additions.length > 0 ? [...previous, ...additions] : previous;
            });
          }
          if (cursor === 0) {
            setHasMore(page.has_more);
            setNextBeforeInteractionId(page.next_before_interaction_id);
          }
          const nextCursor = page.next_after_interaction_id ?? cursor;
          if (nextCursor === cursor) return;
          newestInteractionIdRef.current = nextCursor;
          cursor = nextCursor;
          if (!page.has_more) return;
        } while (!disposed);
      } catch (error) {
        logger.error('Failed to refresh activities, work_id:', workId, error);
      } finally {
        refreshing = false;
      }
    };
    const timer = window.setInterval(() => void refresh(), ACTIVITY_REFRESH_INTERVAL_MS);
    return () => {
      disposed = true;
      window.clearInterval(timer);
    };
  }, [loading, agentId, workId]);

  // Scroll to the latest (bottom) activity record after events are loaded.
  useEffect(() => {
    if (loading || events.length === 0 || !contentRef.current || !scrollToLatestRef.current) return;
    contentRef.current.scrollTop = contentRef.current.scrollHeight;
    scrollToLatestRef.current = false;
  }, [loading, events.length]);

  const toggleExpand = useCallback((eventId: string) => {
    setExpandedEventIds(prev => {
      const next = new Set(prev);
      if (next.has(eventId)) {
        next.delete(eventId);
      } else {
        next.add(eventId);
      }
      return next;
    });
  }, []);

  const loadOlderActivities = useCallback(async () => {
    if (loadingOlder || !hasMore || !nextBeforeInteractionId) return;

    const content = contentRef.current;
    const previousHeight = content?.scrollHeight ?? 0;
    const previousTop = content?.scrollTop ?? 0;
    setLoadingOlder(true);
    try {
      const res = await activityApi.getWorkActivities(agentId, workId, nextBeforeInteractionId);
      setEvents(previous => {
        const existingIDs = new Set(previous.map(event => event.id));
        const olderEvents = res.data.events.filter(event => !existingIDs.has(event.id));
        return [...olderEvents, ...previous];
      });
      setHasMore(res.data.has_more);
      setNextBeforeInteractionId(res.data.next_before_interaction_id);
      setLoadError(false);
      requestAnimationFrame(() => {
        const current = contentRef.current;
        if (current) {
          current.scrollTop = current.scrollHeight - previousHeight + previousTop;
        }
      });
    } catch (error) {
      logger.error('Failed to load older activities, work_id:', workId, error);
      setLoadError(true);
    } finally {
      setLoadingOlder(false);
    }
  }, [hasMore, loadingOlder, nextBeforeInteractionId, agentId, workId]);

  const handleActivityScroll = useCallback(() => {
    if (contentRef.current && contentRef.current.scrollTop <= 16) {
      void loadOlderActivities();
    }
  }, [loadOlderActivities]);

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
          <span>{loadError ? t('activity.loadError') : t('activity.noRecords')}</span>
          {loadError && (
            <button className="activity-retry-btn" onClick={() => void loadLatestActivities()}>
              {t('activity.retry')}
            </button>
          )}
        </div>
      </div>
    );
  }

  return (
    <div className="activity-list">
      <div className="activity-content" ref={contentRef} onScroll={handleActivityScroll}>
        {loadingOlder && <div className="activity-page-loading"><Spin size="small" /></div>}
        {hasMore && !loadingOlder && !loadError && (
          <button className="activity-retry-btn activity-retry-btn-inline" onClick={() => void loadOlderActivities()}>
            {t('activity.loadEarlier')}
          </button>
        )}
        {loadError && (
          <button className="activity-retry-btn activity-retry-btn-inline" onClick={() => void loadOlderActivities()}>
            {t('activity.loadError')} · {t('activity.retry')}
          </button>
        )}
        {events.map(event => {
          if (event.type === 'tool_call') {
            const icon = toolIcon[event.tool || ''] || '🔧';
            const action = t(`activity.tool.${event.tool}`);
            const targetText = formatToolTarget(event, t);
            const needsTruncate = targetText.length > CONTENT_TRUNCATE_LENGTH;
            const expanded = expandedEventIds.has(event.id);

            return (
              <div key={event.id} className="activity-row activity-row-tool_call">
                <div className="activity-row-header">
                  <span className="activity-time">{event.time}</span>
                </div>
                <div className="activity-summary">
                  {icon} {action}
                  {targetText && (needsTruncate && !expanded ? (
                    <span className="activity-tool-target">
                      {' '}{targetText.slice(0, CONTENT_TRUNCATE_LENGTH)}...
                      <button className="activity-expand-btn" onClick={() => toggleExpand(event.id)}>
                        {t('activity.expand')}
                      </button>
                    </span>
                  ) : (
                    <span className="activity-tool-target">
                      {' '}{targetText}
                      {needsTruncate && (
                        <button className="activity-expand-btn" onClick={() => toggleExpand(event.id)}>
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
          const expanded = expandedEventIds.has(event.id);

          return (
            <div key={event.id} className={`activity-row activity-row-${event.type}`}>
              <div className="activity-row-header">
                <span className="activity-time">{event.time}</span>
              </div>
              <div className="activity-summary">
                {needsTruncate && !expanded ? (
                  <>
                    {fullText.slice(0, CONTENT_TRUNCATE_LENGTH)}...
                    <button className="activity-expand-btn" onClick={() => toggleExpand(event.id)}>
                      {t('activity.expand')}
                    </button>
                  </>
                ) : (
                  <>
                    {fullText}
                    {needsTruncate && (
                      <button className="activity-expand-btn" onClick={() => toggleExpand(event.id)}>
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

/**
 * Formats tool targets for humans. Some tools carry internal identifiers in
 * arguments; the Activity timeline should show intent-level summaries instead.
 */
function formatToolTarget(event: ActivityEvent, t: (key: string, options?: Record<string, unknown>) => string): string {
  if (event.tool === 'read_kb_evidence') {
    const count = countNumericIDs(event.target || '');
    return count > 0 ? t('activity.toolTarget.read_kb_evidence', { count }) : '';
  }
  return event.target || '';
}

/**
 * Counts numeric identifiers from both new compact targets ("12") and legacy
 * Go-formatted slices ("[4 19 20]") so old Activity rows render cleanly too.
 */
function countNumericIDs(raw: string): number {
  const text = raw.trim();
  if (!text) return 0;
  if (/^\d+$/.test(text)) return Number(text);
  const matches = text.match(/\d+/g);
  return matches ? matches.length : 0;
}

export default ActivityList;
