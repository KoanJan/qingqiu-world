import { type ReactNode } from 'react';
import { Button, Tooltip } from 'antd';
import { ArrowLeftOutlined, PlusOutlined } from '@ant-design/icons';

interface PanelDetailProps {
  title?: string;
  onBack?: () => void;
  onAdd?: () => void;
  /** Disables the add button (e.g. prerequisite not met yet). */
  onAddDisabled?: boolean;
  /** Tooltip shown on the disabled add button explaining why. */
  onAddTooltip?: string;
  children: ReactNode;
}

/** Detail panel layout with optional back navigation and add button. */
export default function PanelDetail({ title, onBack, onAdd, onAddDisabled, onAddTooltip, children }: PanelDetailProps) {
  return (
    <div className="panel-detail">
      {(title || onBack) && (
        <div className="panel-detail-header">
          <div className="panel-detail-header-left">
            {onBack && (
              <Button
                type="text"
                icon={<ArrowLeftOutlined />}
                onClick={onBack}
                style={{ color: 'var(--color-text-secondary)' }}
              />
            )}
          </div>
          <span className="panel-detail-title">{title}</span>
          <div className="panel-detail-header-right">
            {onAdd && (
              <Tooltip title={onAddDisabled ? onAddTooltip : undefined}>
                <Button
                  type="text"
                  icon={<PlusOutlined />}
                  onClick={onAdd}
                  disabled={onAddDisabled}
                  style={{ color: 'var(--color-text-secondary)' }}
                />
              </Tooltip>
            )}
          </div>
        </div>
      )}
      <div className="panel-detail-body">
        {children}
      </div>
    </div>
  );
}
