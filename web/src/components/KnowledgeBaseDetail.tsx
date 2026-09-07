import React, { useEffect, useState } from 'react';
import { Button, Upload, Tag, message, Empty, Select } from 'antd';
import { UploadOutlined, DeleteOutlined, FileTextOutlined, UserAddOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import type { KnowledgeBase, Document, Agent, KBAccessPerson } from '../types';
import { DOC_STATUS_FAILED, DOC_STATUS_DELETED } from '../types';
import { kbApi, agentApi } from '../services/api';
import { logger } from '../logger';
import { confirmDelete } from '../utils/confirm';
import { formatRelativeTime } from '../utils/time';
import { formatFileSize } from '../utils/format';
import { isAllowedFileExtension } from '../constants/fileTypes';
import AgentAvatar from './AgentAvatar';

/**
 * Props for the KnowledgeBaseDetail component.
 */
interface KnowledgeBaseDetailProps {
  kb: KnowledgeBase;
}

/**
 * Document status configuration map.
 * Maps status int values to their display properties (color and i18n key).
 */
const DOC_STATUS_MAP: Record<number, { color: string; labelKey: string }> = {
  0: { color: 'default', labelKey: 'kb.docStatusPending' },
  1: { color: 'processing', labelKey: 'kb.docStatusProcessing' },
  2: { color: 'success', labelKey: 'kb.docStatusReady' },
  3: { color: 'error', labelKey: 'kb.docStatusFailed' },
  4: { color: 'default', labelKey: 'kb.docStatusDeleted' },
};

/**
 * KnowledgeBaseDetail component displays the detail view of a knowledge base.
 * Shows document list, upload functionality, document management actions,
 * and the authorized agent list (agents allowed to search this KB).
 */
const KnowledgeBaseDetail: React.FC<KnowledgeBaseDetailProps> = ({ kb }) => {
  const { t } = useTranslation();
  const [documents, setDocuments] = useState<Document[]>([]);
  const [loading, setLoading] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [accessList, setAccessList] = useState<KBAccessPerson[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [selectedPersonId, setSelectedPersonId] = useState<number | undefined>(undefined);
  const [granting, setGranting] = useState(false);

  /**
   * Loads the document list for the current knowledge base.
   */
  const loadDocuments = async () => {
    setLoading(true);
    try {
      const response = await kbApi.listDocuments(kb.id);
      setDocuments(response.data);
    } catch (error) {
      logger.error('Failed to load documents:', error);
      message.error(t('messages.loadFailed'));
    } finally {
      setLoading(false);
    }
  };

  /**
   * Loads the authorized agent list for the current knowledge base.
   */
  const loadAccess = async () => {
    try {
      const response = await kbApi.listAccess(kb.id);
      setAccessList(response.data);
    } catch (error) {
      logger.error('Failed to load KB access list:', error);
      message.error(t('messages.loadFailed'));
    }
  };

  /**
   * Loads all agents as candidates for granting access.
   */
  const loadAgents = async () => {
    try {
      const response = await agentApi.list();
      setAgents(response.data);
    } catch (error) {
      logger.error('Failed to load agents:', error);
    }
  };

  useEffect(() => {
    loadDocuments();
    loadAccess();
    loadAgents();
  }, [kb.id]);

  /**
   * Handles document upload with file type validation.
   * @param file - The file to upload
   * @returns false to prevent default upload behavior
   */
  const handleUpload = async (file: File) => {
    // Validate file extension
    if (!isAllowedFileExtension(file.name)) {
      message.error(t('kb.unsupportedFileType'));
      return false;
    }

    setUploading(true);
    try {
      await kbApi.uploadDocument(kb.id, file);
      message.success(t('kb.uploadSuccess'));
      loadDocuments();
    } catch (error) {
      logger.error('Failed to upload document:', error);
      message.error(t('kb.uploadFailed'));
    } finally {
      setUploading(false);
    }
    return false;
  };

  /**
   * Handles document deletion with confirmation dialog.
   * @param docId - The document ID to delete
   */
  const handleDeleteDocument = async (docId: number) => {
    confirmDelete({
      title: t('kb.confirmDeleteDocTitle'),
      content: t('kb.confirmDeleteDoc'),
      okText: t('common.delete'),
      cancelText: t('common.cancel'),
      onOk: async () => {
        try {
          await kbApi.deleteDocument(kb.id, docId);
          message.success(t('kb.deleteDocSuccess'));
          loadDocuments();
        } catch (error) {
          logger.error('Failed to delete document:', error);
          message.error(t('kb.deleteDocFailed'));
        }
      },
    });
  };

  /**
   * Grants search access to the selected agent.
   */
  const handleGrant = async () => {
    if (!selectedPersonId) return;
    setGranting(true);
    try {
      await kbApi.grantAccess(kb.id, selectedPersonId);
      message.success(t('kb.accessGrantSuccess'));
      setSelectedPersonId(undefined);
      loadAccess();
    } catch (error) {
      logger.error('Failed to grant KB access:', error);
      message.error(t('kb.accessGrantFailed'));
    } finally {
      setGranting(false);
    }
  };

  /**
   * Revokes the search access of an authorized agent.
   * @param personId - The agent (AI person) ID to revoke
   */
  const handleRevokeAccess = async (personId: number) => {
    try {
      await kbApi.revokeAccess(kb.id, personId);
      message.success(t('kb.accessRevokeSuccess'));
      setAccessList(accessList.filter(p => p.id !== personId));
    } catch (error) {
      logger.error('Failed to revoke KB access:', error);
      message.error(t('kb.accessRevokeFailed'));
    }
  };

  // Agents not yet granted are the candidates for granting.
  const grantedIds = new Set(accessList.map(p => p.id));
  const candidates = agents.filter(a => !grantedIds.has(a.id));

  return (
    <div className="kb-detail">
      <div className="kb-detail-header">
        <div className="kb-detail-title">{kb.name}</div>
      </div>

      <div className="kb-detail-stats">
        <Tag color={DOC_STATUS_MAP[kb.index_type]?.color || 'default'}>
          {t(DOC_STATUS_MAP[kb.index_type]?.labelKey || String(kb.index_type))}
        </Tag>
        <span className="kb-detail-stat">
          {t('kb.docCount', { count: kb.document_count })}
        </span>
        <span className="kb-detail-stat">
          {t('kb.vectorCount', { count: kb.vector_count })}
        </span>
      </div>

      {kb.description && (
        <div className="kb-detail-desc">{kb.description}</div>
      )}

      <div className="kb-detail-upload">
        <Upload
          accept=".pdf,.txt,.md"
          showUploadList={false}
          beforeUpload={(file) => { handleUpload(file); return false; }}
          disabled={uploading}
        >
          <Button icon={<UploadOutlined />} loading={uploading} size="small">
            {t('kb.uploadDocument')}
          </Button>
        </Upload>
        <div className="kb-upload-hint">
          {t('kb.uploadHint')}
        </div>
      </div>

      <div className="kb-detail-access">
        <div className="kb-access-title">{t('kb.accessTitle')}</div>
        <div className="kb-access-hint">{t('kb.accessHint')}</div>
        {accessList.length > 0 && (
          <div className="kb-access-list">
            {accessList.map(person => (
              <div key={person.id} className="kb-access-item">
                <AgentAvatar avatar={person.avatar} size={20} iconSize={10} borderRadius="50%" />
                <span>{person.name}</span>
                <Button
                  type="text"
                  size="small"
                  danger
                  icon={<DeleteOutlined />}
                  onClick={() => handleRevokeAccess(person.id)}
                  style={{ width: 20, minWidth: 20, height: 20, padding: 0, fontSize: 11 }}
                />
              </div>
            ))}
          </div>
        )}
        <div className="kb-access-grant-row">
          <Select
            size="small"
            style={{ width: 220 }}
            placeholder={candidates.length === 0 ? t('kb.accessNoCandidates') : t('kb.accessSelectPlaceholder')}
            value={selectedPersonId}
            onChange={(v) => setSelectedPersonId(v)}
            disabled={candidates.length === 0}
            options={candidates.map(a => ({ value: a.id, label: a.name }))}
          />
          <Button
            size="small"
            icon={<UserAddOutlined />}
            onClick={handleGrant}
            loading={granting}
            disabled={!selectedPersonId}
          >
            {t('kb.accessGrant')}
          </Button>
        </div>
      </div>

      <div className="kb-detail-docs">
        {loading ? (
          <div className="empty-state-text">{t('sidebar.loading')}</div>
        ) : documents.length === 0 ? (
          <Empty description={t('kb.noDocuments')} image={Empty.PRESENTED_IMAGE_SIMPLE} />
        ) : (
          documents.map(doc => (
            <div key={doc.id} className="kb-doc-item">
              <FileTextOutlined style={{ color: 'var(--color-text-secondary)', fontSize: 16, flexShrink: 0 }} />
              <div className="kb-doc-info">
                <div className="kb-doc-title">{doc.title}</div>
                <div className="kb-doc-meta">
                  <Tag
                    color={DOC_STATUS_MAP[doc.status]?.color || 'default'}
                    style={{ fontSize: 11, lineHeight: '16px', padding: '0 4px' }}
                  >
                    {t(DOC_STATUS_MAP[doc.status]?.labelKey || String(doc.status))}
                  </Tag>
                  {doc.chunk_count > 0 && <span>{doc.chunk_count} chunks</span>}
                  {doc.file_size > 0 && <span>{formatFileSize(doc.file_size)}</span>}
                  <span>{formatRelativeTime(doc.created_at)}</span>
                </div>
                {doc.status === DOC_STATUS_FAILED && doc.error_message && (
                  <div className="kb-doc-error">{doc.error_message}</div>
                )}
              </div>
              {doc.status !== DOC_STATUS_DELETED && (
                <Button
                  type="text"
                  size="small"
                  danger
                  icon={<DeleteOutlined />}
                  onClick={() => handleDeleteDocument(doc.id)}
                  style={{ flexShrink: 0 }}
                />
              )}
            </div>
          ))
        )}
      </div>
    </div>
  );
};

export default KnowledgeBaseDetail;
