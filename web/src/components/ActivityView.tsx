import { useCallback, useEffect, useRef, useState } from 'react';
import { ChevronDown, ChevronLeft, ChevronRight, ChevronUp, Timeline } from 'lucide-react';
import { Spin } from 'antd';
import { useTranslation } from 'react-i18next';
import AgentAvatar from './AgentAvatar';
import ActivityList from './ActivityList';
import ResizableCard from './ResizableCard';
import { activityApi } from '../services/api';
import { logger } from '../logger';
import type { ActivityAgent, ActivityWorkspace, ActivityWork } from '../types';

const REFRESH_INTERVAL_MS = 15_000;

/** ActivityView browses Focus history by agent, workspace, then Work. */
export default function ActivityView({ active }: { active: boolean }) {
  const { t } = useTranslation();
  const agentStripRef = useRef<HTMLDivElement>(null);
  const [agents, setAgents] = useState<ActivityAgent[]>([]);
  const [agentID, setAgentID] = useState<number | null>(null);
  const [agentsLoading, setAgentsLoading] = useState(true);
  const [agentsError, setAgentsError] = useState(false);
  const [workspaces, setWorkspaces] = useState<ActivityWorkspace[]>([]);
  const [selectedWorkspace, setSelectedWorkspace] = useState<ActivityWorkspace | null>(null);
  const [workspacesLoading, setWorkspacesLoading] = useState(false);
  const [workspacesError, setWorkspacesError] = useState(false);
  const [workspacePage, setWorkspacePage] = useState(1);
  const [hasMoreWorkspaces, setHasMoreWorkspaces] = useState(false);
  const [works, setWorks] = useState<ActivityWork[]>([]);
  const [worksLoading, setWorksLoading] = useState(false);
  const [worksError, setWorksError] = useState(false);
  const [hasMoreWorks, setHasMoreWorks] = useState(false);
  const [nextBeforeWorkID, setNextBeforeWorkID] = useState(0);
  const [expandedWorkID, setExpandedWorkID] = useState<number | null>(null);
  const workspaceRequestRef = useRef(0);
  const workRequestRef = useRef(0);
  const hasWorkspaceDataRef = useRef(false);
  const hasWorkDataRef = useRef(false);

  const selectedAgent = agents.find(agent => agent.id === agentID) ?? null;
  const selectedWorkspaceID = selectedWorkspace?.id ?? null;

  useEffect(() => {
    const strip = agentStripRef.current;
    const selected = strip?.querySelector<HTMLElement>('.activity-agent-option.selected');
    if (!strip || !selected) return;
    // Only scroll the agent strip; scrollIntoView also moves the page viewport
    // while the Activity page is sliding into place.
    const stripBounds = strip.getBoundingClientRect();
    const selectedBounds = selected.getBoundingClientRect();
    if (selectedBounds.left < stripBounds.left) {
      strip.scrollBy({ left: selectedBounds.left - stripBounds.left, behavior: 'smooth' });
    } else if (selectedBounds.right > stripBounds.right) {
      strip.scrollBy({ left: selectedBounds.right - stripBounds.right, behavior: 'smooth' });
    }
  }, [agentID]);

  const loadAgents = useCallback(async () => {
    try {
      const result = await activityApi.listAgents();
      setAgents(result.data ?? []);
      setAgentID(previous => result.data.some(agent => agent.id === previous) ? previous : result.data[0]?.id ?? null);
      setAgentsError(false);
    } catch (error) {
      logger.error('Failed to load Activity agents', error);
      setAgentsError(true);
    } finally {
      setAgentsLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!active) return;
    void loadAgents();
    const timer = window.setInterval(() => void loadAgents(), REFRESH_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [active, loadAgents]);

  const loadWorkspaces = useCallback(async (personID: number, page: number, refresh = false) => {
    const requestID = ++workspaceRequestRef.current;
    if (!refresh) setWorkspacesLoading(true);
    try {
      const result = await activityApi.listWorkspaces(personID, page);
      if (requestID !== workspaceRequestRef.current) return;
      setWorkspaces(previous => {
        if (page === 1 && !refresh) return result.data.workspaces;
        const retained = page === 1 ? previous.filter(item => !result.data.workspaces.some(next => next.id === item.id)) : previous;
        return page === 1 ? [...result.data.workspaces, ...retained] : [...retained, ...result.data.workspaces];
      });
      hasWorkspaceDataRef.current = result.data.workspaces.length > 0;
      if (!refresh) {
        setWorkspacePage(page);
        setHasMoreWorkspaces(result.data.has_more);
      }
      setWorkspacesError(false);
      if (page === 1 && !refresh) {
        setSelectedWorkspace(previous => previous ?? result.data.workspaces[0] ?? null);
      }
    } catch (error) {
      if (requestID !== workspaceRequestRef.current) return;
      logger.error('Failed to load Activity workspaces', personID, error);
      setWorkspacesError(true);
    } finally {
      if (requestID === workspaceRequestRef.current) setWorkspacesLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!active || agentID === null) return;
    const requestTracker = workspaceRequestRef;
    void loadWorkspaces(agentID, 1, hasWorkspaceDataRef.current);
    const timer = window.setInterval(() => void loadWorkspaces(agentID, 1, true), REFRESH_INTERVAL_MS);
    return () => {
      requestTracker.current++;
      window.clearInterval(timer);
    };
  }, [active, agentID, loadWorkspaces]);

  const loadWorks = useCallback(async (personID: number, workspaceID: number, beforeWorkID = 0, refresh = false) => {
    const requestID = ++workRequestRef.current;
    if (!refresh) setWorksLoading(true);
    try {
      const result = await activityApi.listWorks(personID, workspaceID, beforeWorkID || undefined);
      if (requestID !== workRequestRef.current) return;
      for (const work of result.data.works) {
        if (![0, 1, 2, -1].includes(work.status)) {
          logger.error('Activity work has unexpected status', work.id, work.status);
        }
      }
      setWorks(previous => {
        if (!beforeWorkID && !refresh) return result.data.works;
        const retained = previous.filter(item => !result.data.works.some(next => next.id === item.id));
        return beforeWorkID ? [...retained, ...result.data.works] : [...result.data.works, ...retained];
      });
      hasWorkDataRef.current = result.data.works.length > 0;
      if (!refresh) {
        setHasMoreWorks(result.data.has_more);
        setNextBeforeWorkID(result.data.next_before_work_id);
      }
      setWorksError(false);
      if (!beforeWorkID) setExpandedWorkID(previous => previous ?? result.data.works[0]?.id ?? null);
    } catch (error) {
      if (requestID !== workRequestRef.current) return;
      logger.error('Failed to load Activity works', personID, workspaceID, error);
      setWorksError(true);
    } finally {
      if (requestID === workRequestRef.current) setWorksLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!active || agentID === null || selectedWorkspaceID === null) return;
    const requestTracker = workRequestRef;
    void loadWorks(agentID, selectedWorkspaceID, 0, hasWorkDataRef.current);
    const timer = window.setInterval(() => void loadWorks(agentID, selectedWorkspaceID, 0, true), REFRESH_INTERVAL_MS);
    return () => {
      requestTracker.current++;
      window.clearInterval(timer);
    };
  }, [active, agentID, selectedWorkspaceID, loadWorks]);

  const chooseAgent = (id: number) => {
    if (id === agentID) return;
    setAgentID(id);
    setWorkspaces([]);
    hasWorkspaceDataRef.current = false;
    setSelectedWorkspace(null);
    setWorks([]);
    hasWorkDataRef.current = false;
  };

  const chooseWorkspace = (workspace: ActivityWorkspace) => {
    if (workspace.id === selectedWorkspaceID) {
      if (!worksLoading && agentID !== null) void loadWorks(agentID, workspace.id);
      return;
    }
    setWorkspaces(previous => previous.some(item => item.id === workspace.id) ? previous : [...previous, workspace]);
    setSelectedWorkspace(workspace);
    setWorks([]);
    hasWorkDataRef.current = false;
    setExpandedWorkID(null);
  };

  return (
    <div className="activity-view">
      <ResizableCard defaultWidth={280} minWidth={220} maxWidth={400} resizeSide="right" className="app-sidebar-wrapper">
        <div className="activity-sidebar">
          <div className="activity-agent-header">
            <button type="button" className="activity-strip-arrow" aria-label={t('activity.previousAgent')} onClick={() => agentStripRef.current?.scrollBy({ left: -150, behavior: 'smooth' })}><ChevronLeft size={16} /></button>
            <div className="activity-agent-strip" ref={agentStripRef} aria-label={t('activity.agents')}>
              {agents.map(agent => (
                <button key={agent.id} type="button" className={`activity-agent-option${agentID === agent.id ? ' selected' : ''}${agent.status === 2 ? ' deceased' : ''}`} aria-label={agent.status === 2 ? `${agent.name} · ${t('activity.deceased')}` : agent.name} aria-pressed={agentID === agent.id} title={agent.name} onClick={() => chooseAgent(agent.id)}>
                  <span className="activity-agent-avatar">
                    <AgentAvatar avatar={agent.avatar} size={28} iconSize={15} borderRadius="7px" />
                    {agent.has_active_work && <span className="activity-live-dot" />}
                  </span>
                </button>
              ))}
            </div>
            <button type="button" className="activity-strip-arrow" aria-label={t('activity.nextAgent')} onClick={() => agentStripRef.current?.scrollBy({ left: 150, behavior: 'smooth' })}><ChevronRight size={16} /></button>
          </div>
          {agentsLoading ? <div className="activity-sidebar-state"><Spin size="small" /></div>
            : agentsError ? <div className="activity-sidebar-state">{t('activity.loadError')}<button onClick={() => void loadAgents()}>{t('activity.retry')}</button></div>
              : agents.length === 0 ? <div className="activity-sidebar-state">{t('sidebar.noAgent')}</div>
                : <>
                  <div className="activity-sidebar-label">{t('activity.workspaces')}</div>
                  <div className="activity-workspace-list">
                    {workspacesLoading && workspaces.length === 0 ? <div className="activity-sidebar-state"><Spin size="small" /></div>
                      : workspaces.length === 0 ? <div className="activity-sidebar-state">{workspacesError ? t('activity.loadError') : t('activity.noWorkspaces')}</div>
                        : workspaces.map(workspace => (
                          <button key={workspace.id} type="button" className={`activity-workspace-option${selectedWorkspace?.id === workspace.id ? ' selected' : ''}`} onClick={() => chooseWorkspace(workspace)}>
                            <span className="activity-workspace-name">{workspace.name}</span>
                            {workspace.has_active_work && <span className="activity-live-dot" title={t('activity.running')} />}
                            {workspace.purpose && <span className="activity-workspace-purpose">{workspace.purpose}</span>}
                          </button>
                        ))}
                    {workspacesError && <button className="activity-text-button" onClick={() => agentID !== null && void loadWorkspaces(agentID, 1)}>{t('activity.retry')}</button>}
                    {hasMoreWorkspaces && !workspacesLoading && <button className="activity-text-button" onClick={() => agentID !== null && void loadWorkspaces(agentID, workspacePage + 1)}>{t('activity.loadMore')}</button>}
                  </div>
                </>}
        </div>
      </ResizableCard>
      <div className="app-content">
        <ResizableCard flex className="app-chat-area-wrapper">
          <div className="activity-main">
            {selectedAgent && selectedWorkspace ? <>
              <div className="activity-main-header">
                <div className="activity-main-title">{selectedWorkspace.name}</div>
                {selectedWorkspace.purpose && <div className="activity-main-purpose">{selectedWorkspace.purpose}</div>}
              </div>
              <div className="activity-work-list">
                {worksLoading && works.length === 0 ? <div className="activity-empty"><Spin size="small" /></div>
                  : works.length === 0 ? <div className="activity-empty">{worksError ? t('activity.loadError') : t('activity.noRecords')}</div>
                    : works.map(work => {
                      const primary = work.default_workspace_id === selectedWorkspace.id;
                      const expanded = expandedWorkID === work.id;
                      return <section className="activity-work-card" key={work.id}>
                        <button type="button" className="activity-work-heading" onClick={() => setExpandedWorkID(expanded ? null : work.id)} aria-expanded={expanded}>
                          <span className="activity-work-description">{work.description}</span>
                          <span className={`activity-work-status${work.status === 0 ? ' running' : ''}`}>{work.status === 0 ? t('activity.running') : work.status === 1 ? t('activity.completed') : work.status === 2 ? t('activity.failed') : work.status === -1 ? t('activity.stopped') : t('activity.unknownStatus')}</span>
                          {expanded ? <ChevronUp size={16} /> : <ChevronDown size={16} />}
                        </button>
                        <div className="activity-work-time">{new Date(work.created_at).toLocaleString()}</div>
                        {expanded && (primary
                          ? <div className="activity-work-events"><ActivityList key={work.id} workId={work.id} agentId={selectedAgent.id} /></div>
                          : <div className="activity-work-reference">
                            {t('activity.alsoUsedWorkspace')}
                            <button type="button" className="activity-text-button" onClick={() => chooseWorkspace({ id: work.default_workspace_id, name: work.default_workspace_name, purpose: '', has_active_work: work.status === 0 })}>
                              {t('activity.viewPrimaryWorkspace', { name: work.default_workspace_name })}
                            </button>
                          </div>)}
                      </section>;
                    })}
                {worksError && <button className="activity-text-button" onClick={() => void loadWorks(selectedAgent.id, selectedWorkspace.id)}>{t('activity.retry')}</button>}
                {hasMoreWorks && !worksLoading && <button className="activity-text-button" onClick={() => void loadWorks(selectedAgent.id, selectedWorkspace.id, nextBeforeWorkID)}>{t('activity.loadEarlier')}</button>}
              </div>
            </> : <div className="activity-empty"><Timeline size={22} />{t('activity.selectWorkspace')}</div>}
          </div>
        </ResizableCard>
      </div>
    </div>
  );
}
