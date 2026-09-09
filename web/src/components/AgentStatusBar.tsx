import React from 'react';
import AgentAvatar from './AgentAvatar';
import type { SessionAgentStatus } from '../types';
import { PARTICIPANT_STATUS_IDLE, PARTICIPANT_STATUS_WORKING } from '../types';
import { logger } from '../logger';

/**
 * AgentStatusBar displays a horizontal bar of agent avatars
 * with status indicators. Positioned above the chat messages area.
 *
 * Each agent avatar has a small dot in the top-right corner:
 * - idle (0): green dot (static)
 * - working (1): pulsing blue dot
 *
 * Agent name is shown on hover via a tooltip-like label.
 */
interface AgentStatusBarProps {
  agents: SessionAgentStatus[];
}

const STATUS_DOT_CONFIG: Record<number, { color: string; animate: boolean; label: string }> = {
  [PARTICIPANT_STATUS_IDLE]:    { color: '#22c55e', animate: false, label: 'Idle' },
  [PARTICIPANT_STATUS_WORKING]: { color: '#3b82f6', animate: true,  label: 'Thinking...' },
};

// Default config for unknown status values
const DEFAULT_DOT_CONFIG = { color: '#9ca3af', animate: false, label: 'Unknown' };

const AgentStatusBar: React.FC<AgentStatusBarProps> = ({ agents }) => {
  if (!agents || agents.length === 0) return null;

  return (
    <>
      <div className="agent-status-bar">
        {agents.map((agent) => {
          const config = STATUS_DOT_CONFIG[agent.status];
          if (!config) {
            logger.warn('Unknown agent status, using default', 'status', agent.status, 'agent_id', agent.agent_id);
          }
          const dotConfig = config ?? DEFAULT_DOT_CONFIG;
          return (
            <div key={agent.agent_id} className="agent-status-item">
              <div className="agent-avatar-wrapper">
                <AgentAvatar avatar={agent.avatar} size={28} iconSize={14} borderRadius="50%" />
                <span
                  className={`agent-status-dot ${dotConfig.animate ? 'agent-status-dot-animated' : ''}`}
                  style={{ backgroundColor: dotConfig.color }}
                />
              </div>
              <span className="agent-status-name">{agent.name}</span>
            </div>
          );
        })}
      </div>
      <hr className="agent-status-divider" />
    </>
  );
};

export default AgentStatusBar;
