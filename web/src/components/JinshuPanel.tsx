import React, { useEffect, useRef, useState } from 'react';
import { Spin, Empty, Select, Input, Upload, Button, Form, message } from 'antd';
import { CaretRightOutlined, CaretDownOutlined, FolderOpenOutlined, FolderOutlined, FileOutlined, DesktopOutlined, UploadOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { jinshuApi, agentApi } from '../services/api';
import { logger } from '../logger';
import PanelDetail from './PanelDetail';
import type { Jinshu, JinshuFileEntry, Agent } from '../types';
import { formatFileSize } from '../utils/format';
import { formatMessageTime } from '../utils/time';

type Direction = 'sent' | 'received';

// countTreeFiles returns the number of files in a jinshu file tree.
const countTreeFiles = (nodes: JinshuFileEntry[]): number => {
  let count = 0;
  for (const node of nodes) {
    if (node.is_dir) {
      count += countTreeFiles(node.children ?? []);
    } else {
      count++;
    }
  }
  return count;
};

// Recursive file tree renderer.
const FileTreeRenderer: React.FC<{
  nodes: JinshuFileEntry[];
  depth: number;
}> = ({ nodes, depth }) => {
  return (
    <>
      {nodes.map((node) => (
        <TreeNodeItem
          key={node.path}
          node={node}
          depth={depth}
        />
      ))}
    </>
  );
};

const TreeNodeItem: React.FC<{
  node: JinshuFileEntry;
  depth: number;
}> = ({ node, depth }) => {
  const [expanded, setExpanded] = useState(true);

  if (node.is_dir) {
    return (
      <div className="received-tree-dir">
        <div
          className="received-tree-item received-tree-dir-row"
          style={{ paddingLeft: depth * 16 }}
          onClick={() => setExpanded(!expanded)}
        >
          {expanded ? <CaretDownOutlined className="tree-caret" /> : <CaretRightOutlined className="tree-caret" />}
          {expanded ? <FolderOpenOutlined className="file-icon" /> : <FolderOutlined className="file-icon" />}
          <span className="file-name">{node.name}/</span>
        </div>
        {expanded && node.children && node.children.length > 0 && (
          <FileTreeRenderer
            nodes={node.children}
            depth={depth + 1}
          />
        )}
      </div>
    );
  }

  return (
    <div
      className="received-tree-item received-tree-file-row"
      style={{ paddingLeft: depth * 16 + 20 }}
    >
      <FileOutlined className="file-icon" />
      <span className="file-name" title={node.path}>{node.name}</span>
      {node.size !== undefined && node.size > 0 && (
        <span className="file-size">{formatFileSize(node.size)}</span>
      )}
    </div>
  );
};

// JinshuDetailContent renders the full detail of a single jinshu record,
// reached by clicking a list row (instead of an inline expand, which gets
// unwieldy when there are many files). The surrounding PanelDetail (title +
// back button) is provided by JinshuPanel.
const JinshuDetailContent: React.FC<{
  item: Jinshu;
  direction: Direction;
  onOpenDir: (item: Jinshu) => void;
}> = ({ item, direction, onOpenDir }) => {
  const { t } = useTranslation();
  const files = item.files ?? [];
  const totalFiles = countTreeFiles(files);
  const counterpart = direction === 'sent' ? item.to_name : item.from_name;
  const counterpartLabel = direction === 'sent' ? t('jinshu.to') : t('jinshu.from');

  return (
    <div className="jinshu-detail">
      <div className="jinshu-detail-meta">
        <span className="jinshu-counterpart">{counterpartLabel} {counterpart}</span>
        <span className="jinshu-time">{formatMessageTime(new Date(item.created_at))}</span>
        <span className="delivery-file-count">{totalFiles} file{totalFiles > 1 ? 's' : ''}</span>
        <button
          className="delivery-open-dir-btn"
          onClick={() => onOpenDir(item)}
          title="Show in Finder"
        >
          <DesktopOutlined />
        </button>
      </div>
      {item.description && (
        <div className="jinshu-description">{item.description}</div>
      )}
      <div className="jinshu-files">
        <div className="jinshu-files-title">{t('jinshu.files')}</div>
        <FileTreeRenderer
          nodes={files}
          depth={0}
        />
      </div>
    </div>
  );
};

// JinshuSendForm renders the form for sending a jinshu from the current user
// to an agent. Receivers are loaded from /api/agents (agent id == person id).
const JinshuSendForm: React.FC<{
  onBack: () => void;
  onDone: () => void;
}> = ({ onBack, onDone }) => {
  const { t } = useTranslation();
  const [agents, setAgents] = useState<Agent[]>([]);
  const [files, setFiles] = useState<File[]>([]);
  const [sending, setSending] = useState(false);

  useEffect(() => {
    agentApi
      .list()
      .then((res) => setAgents(res.data))
      .catch((error) => logger.error('Failed to load agents for jinshu', error));
  }, []);

  const fileList = files.map((file, index) => ({
    uid: String(index),
    name: file.name,
    status: 'done' as const,
  }));

  const handleSubmit = async (values: { receiver: number; topic: string; description?: string }) => {
    if (files.length === 0) {
      message.error(t('jinshu.filesRequired'));
      return;
    }

    setSending(true);
    try {
      await jinshuApi.send({
        to_person_id: values.receiver,
        topic: values.topic.trim(),
        description: values.description?.trim() || undefined,
        files,
      });
      message.success(t('jinshu.sendSuccess'));
      onDone();
    } catch (error) {
      logger.error('Failed to send jinshu', error);
      message.error(t('jinshu.sendFailed'));
    } finally {
      setSending(false);
    }
  };

  return (
    <Form
      layout="vertical"
      className="jinshu-send-form"
      onFinish={handleSubmit}
    >
      <Form.Item
        label={t('jinshu.receiver')}
        name="receiver"
        rules={[{ required: true, message: t('jinshu.receiverRequired') }]}
      >
        <Select
          placeholder={t('jinshu.receiverPlaceholder')}
          options={agents.map((a) => ({ value: a.id, label: a.name }))}
          showSearch
          optionFilterProp="label"
        />
      </Form.Item>

      <Form.Item
        label={t('jinshu.topic')}
        name="topic"
        rules={[{ required: true, message: t('jinshu.topicRequired') }]}
      >
        <Input placeholder={t('jinshu.topicPlaceholder')} />
      </Form.Item>

      <Form.Item label={t('jinshu.description')} name="description">
        <Input.TextArea placeholder={t('jinshu.descriptionPlaceholder')} rows={4} />
      </Form.Item>

      <Form.Item label={t('jinshu.files')} required>
        <Upload
          multiple
          fileList={fileList}
          beforeUpload={(file) => {
            setFiles((prev) => [...prev, file]);
            return false;
          }}
          onRemove={(file) => {
            const index = Number(file.uid);
            setFiles((prev) => prev.filter((_, i) => i !== index));
            return true;
          }}
        >
          <Button icon={<UploadOutlined />}>{t('jinshu.selectFiles')}</Button>
        </Upload>
      </Form.Item>

      <div className="jinshu-send-actions">
        <Button onClick={onBack}>{t('common.cancel')}</Button>
        <Button type="primary" htmlType="submit" loading={sending}>
          {t('jinshu.sendButton')}
        </Button>
      </div>
    </Form>
  );
};

interface JinshuPanelProps {
  direction: Direction;
}

const JinshuPanel: React.FC<JinshuPanelProps> = ({ direction }) => {
  const { t } = useTranslation();
  const [items, setItems] = useState<Jinshu[]>([]);
  const [loading, setLoading] = useState(true);
  const [selected, setSelected] = useState<Jinshu | null>(null);
  const [showSend, setShowSend] = useState(false);
  const prevDataRef = useRef<string>('');

  const load = async (isPoll: boolean = false) => {
    if (!isPoll) {
      setLoading(true);
    }
    try {
      const response = direction === 'sent'
        ? await jinshuApi.listSent()
        : await jinshuApi.listReceived();
      const data = response.data;
      const json = JSON.stringify(data);
      // Skip re-render if nothing changed (prevents flicker on poll).
      if (json === prevDataRef.current) {
        return;
      }
      prevDataRef.current = json;
      setItems(data);
    } catch (error) {
      logger.error('Failed to load jinshu', error);
    } finally {
      if (!isPoll) {
        setLoading(false);
      }
    }
  };

  useEffect(() => {
    // Reset detail selection, send form, and cached data when switching direction.
    setSelected(null);
    setShowSend(false);
    prevDataRef.current = '';
    load(false);
    const timer = setInterval(() => load(true), 10000);
    return () => clearInterval(timer);
  }, [direction]);

  const handleOpenDir = async (item: Jinshu) => {
    const firstPath = (item.files ?? [])[0]?.local_path;
    if (!firstPath) return;
    const dirPath = firstPath.substring(0, firstPath.lastIndexOf('/'));
    if (window.electronAPI) {
      const error = await window.electronAPI.openPath(dirPath);
      if (error) {
        logger.error('[JinshuPanel] openPath dir failed:', error);
      }
    }
  };

  // After a successful send, return to the list and refresh it.
  const handleSendDone = () => {
    setShowSend(false);
    prevDataRef.current = '';
    load(false);
  };

  // Open a jinshu detail and mark received jinshu as read (receiver-only flag).
  const handleSelect = async (item: Jinshu) => {
    setSelected(item);
    if (direction === 'received' && item.is_read === false) {
      try {
        await jinshuApi.markRead(item.id);
        const markRead = (it: Jinshu) => (it.id === item.id ? { ...it, is_read: true } : it);
        setItems((prev) => prev.map(markRead));
        setSelected((prev) => (prev && prev.id === item.id ? { ...prev, is_read: true } : prev));
      } catch (error) {
        logger.error('Failed to mark jinshu read', error);
      }
    }
  };

  const listTitle = direction === 'sent' ? t('jinshu.sent') : t('jinshu.received');

  if (loading) {
    return (
      <PanelDetail title={listTitle}>
        <div className="jinshu-empty">
          <Spin size="large" />
        </div>
      </PanelDetail>
    );
  }

  if (showSend) {
    return (
      <PanelDetail title={t('jinshu.send')} onBack={() => setShowSend(false)}>
        <JinshuSendForm
          onBack={() => setShowSend(false)}
          onDone={handleSendDone}
        />
      </PanelDetail>
    );
  }

  if (selected) {
    return (
      <PanelDetail title={selected.topic || t('jinshu.noTopic')} onBack={() => setSelected(null)}>
        <JinshuDetailContent
          item={selected}
          direction={direction}
          onOpenDir={handleOpenDir}
        />
      </PanelDetail>
    );
  }

  return (
    <PanelDetail
      title={listTitle}
      onAdd={direction === 'sent' ? () => setShowSend(true) : undefined}
    >
      {items.length === 0 ? (
        <div className="jinshu-empty">
          <Empty description={t('jinshu.empty')} />
        </div>
      ) : (
        <div className="received-list">
          {items.map((item) => {
            // Row layout: counterpart (14%) | topic (flexible) | read status
            // (12%, received only) | time (20%). Unread received rows render
            // every field bold.
            const counterpart = direction === 'sent' ? item.to_name : item.from_name;
            const unread = direction === 'received' && item.is_read === false;
            return (
              <div key={item.id} className="received-delivery-item">
                <button
                  className={`received-delivery-header${unread ? ' unread' : ''}`}
                  onClick={() => handleSelect(item)}
                >
                  <span className="jinshu-counterpart">{counterpart}</span>
                  <span className="jinshu-topic">{item.topic || t('jinshu.noTopic')}</span>
                  {direction === 'received' && (
                    <span className={`jinshu-read-status${unread ? ' unread' : ''}`}>
                      {unread ? t('jinshu.unread') : t('jinshu.read')}
                    </span>
                  )}
                  <span className="jinshu-time">{formatMessageTime(new Date(item.created_at))}</span>
                </button>
              </div>
            );
          })}
        </div>
      )}
    </PanelDetail>
  );
};

export default JinshuPanel;
