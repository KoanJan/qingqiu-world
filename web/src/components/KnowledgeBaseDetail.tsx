import React, { useEffect, useState } from 'react';
import { Button, Upload, Tag, message, Empty, Slider, Tooltip, Tabs } from 'antd';
import { UploadOutlined, FileTextOutlined, EditOutlined, CheckOutlined, CloseOutlined } from '@ant-design/icons';
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
import CardActions from './CardActions';

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
 * The body is a tabbed layout: "Documents" holds the document list and upload,
 * "Settings" holds hybrid retrieval configuration and authorized-agent access.
 */
const KnowledgeBaseDetail: React.FC<KnowledgeBaseDetailProps> = ({ kb }) => {
  const { t } = useTranslation();
  const [documents, setDocuments] = useState<Document[]>([]);
  const [loading, setLoading] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [accessList, setAccessList] = useState<KBAccessPerson[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  // Access editing mode: while on, every agent shows as a checkable pill.
  const [editingAccess, setEditingAccess] = useState(false);
  // Draft selection while editing: the agent IDs currently checked. Changes
  // stay local until "done" commits them as one batch.
  const [draftIds, setDraftIds] = useState<Set<number>>(new Set());
  // Whether the batch access commit is in flight.
  const [savingAccess, setSavingAccess] = useState(false);
  const [keywordRatio, setKeywordRatio] = useState<number>(kb.keyword_ratio);
  // Last successfully persisted value — the rollback target when a save fails.
  const [savedRatio, setSavedRatio] = useState<number>(kb.keyword_ratio);

  // Re-sync the local ratio state when switching to another KB.
  useEffect(() => {
    setKeywordRatio(kb.keyword_ratio);
    setSavedRatio(kb.keyword_ratio);
  }, [kb.id, kb.keyword_ratio]);

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
   * Loads all agents as the candidates for access editing.
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
    // Leave access editing mode and drop the draft when switching KBs.
    setEditingAccess(false);
    setDraftIds(new Set());
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
   * Enters access editing: snapshots the persisted grants into the draft.
   */
  const handleStartAccessEdit = () => {
    setDraftIds(new Set(accessList.map(p => p.id)));
    setEditingAccess(true);
  };

  /**
   * Leaves access editing without saving: the draft is discarded and the
   * persisted access list stays untouched.
   */
  const handleCancelAccessEdit = () => {
    setDraftIds(new Set());
    setEditingAccess(false);
  };

  /**
   * Toggles one agent in the draft selection. No server call here; the
   * change is committed as a batch when the user hits "done".
   * @param personId - The agent (AI person) ID to toggle
   */
  const handleToggleDraft = (personId: number) => {
    if (savingAccess) return;
    setDraftIds(prev => {
      const next = new Set(prev);
      if (next.has(personId)) {
        next.delete(personId);
      } else {
        next.add(personId);
      }
      return next;
    });
  };

  /**
   * Commits the draft selection: diffs it against the persisted access list
   * and submits the needed grant/revoke calls as one batch. Re-fetches the
   * list afterwards so local state always mirrors the server.
   */
  const handleCommitAccessEdit = async () => {
    if (savingAccess) return;
    setSavingAccess(true);
    try {
      const persisted = new Set(accessList.map(p => p.id));
      const toGrant = [...draftIds].filter(id => !persisted.has(id));
      const toRevoke = [...persisted].filter(id => !draftIds.has(id));
      const results = await Promise.allSettled([
        ...toGrant.map(id => kbApi.grantAccess(kb.id, id)),
        ...toRevoke.map(id => kbApi.revokeAccess(kb.id, id)),
      ]);
      let failedCount = 0;
      for (const result of results) {
        if (result.status === 'rejected') {
          failedCount += 1;
          logger.error('Failed to commit KB access change:', result.reason);
        }
      }
      if (failedCount > 0) {
        message.error(t('kb.accessSaveFailed'));
      } else if (toGrant.length + toRevoke.length > 0) {
        message.success(t('kb.accessSaveSuccess'));
      }
      handleCancelAccessEdit();
      await loadAccess();
    } finally {
      setSavingAccess(false);
    }
  };

  /**
   * Persists the hybrid-retrieval keyword ratio (alpha) for this KB. Called
   * when the slider drag ends; rolls back to the last persisted value on
   * failure so the slider never shows an unsaved state as if it were saved.
   */
  const handleSaveKeywordRatio = async (value: number) => {
    try {
      await kbApi.update(kb.id, { keyword_ratio: value });
      setSavedRatio(value);
      message.success(t('kb.updateSuccess'));
    } catch (error) {
      logger.error('Failed to update keyword ratio:', error);
      message.error(t('kb.updateFailed'));
      setKeywordRatio(savedRatio);
    }
  };

  // Rail split position (%): the blue segment (vector weight, 1 - ratio)
  // spans from the left edge to the split, the pink segment (keyword weight)
  // takes the rest — the handle rides exactly on the split.
  const splitPos = ((1 - keywordRatio) * 100).toFixed(2);
  // End-of-rail percentages; keyword is the base so they always sum to 100.
  const keywordPct = Math.round(keywordRatio * 100);
  const vectorPct = 100 - keywordPct;

  return (
    <div className="kb-detail">
      {/* Header: identity + meta; the tabs below carry the divider */}
      <div className="kb-detail-header">
        <div className="kb-detail-title">{kb.name}</div>
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
      </div>

      {/* Tabbed body: documents | settings */}
      <Tabs
        className="kb-detail-tabs"
        defaultActiveKey="documents"
        items={[
          {
            key: 'documents',
            label: t('kb.docsTitle'),
            children: (
              <div className="kb-section">
                <div className="kb-section-header kb-section-header-end">
                  <Tooltip title={t('kb.uploadHint')}>
                    <Upload
                      accept=".pdf,.txt,.md"
                      showUploadList={false}
                      beforeUpload={(file) => { handleUpload(file); return false; }}
                      disabled={uploading}
                    >
                      <Button type="text" size="small" shape="circle" icon={<UploadOutlined />} loading={uploading} />
                    </Upload>
                  </Tooltip>
                </div>
                <div className="kb-doc-list">
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
                          <CardActions onDelete={() => handleDeleteDocument(doc.id)} />
                        )}
                      </div>
                    ))
                  )}
                </div>
              </div>
            ),
          },
          {
            key: 'settings',
            label: t('kb.settingsTab'),
            children: (
              <>
                {/* Hybrid retrieval */}
                <div className="kb-section">
                  <div className="kb-section-header">
                    <Tooltip title={t('kb.keywordRatioHint')}>
                      <div className="kb-section-title">{t('kb.hybridRetrieval')}</div>
                    </Tooltip>
                  </div>
                  {/* Color, not distance, carries the semantics: the left
                      (blue) segment is the vector share, the right (pink)
                      segment is the keyword share, and the handle rides on
                      the split point. The slider value is therefore the
                      vector weight (1 - keywordRatio). */}
                  <Slider
                    min={0}
                    max={1}
                    step={0.01}
                    value={1 - keywordRatio}
                    onChange={(v) => setKeywordRatio(1 - (v as number))}
                    onChangeComplete={(v) => handleSaveKeywordRatio(1 - v)}
                    styles={{
                      track: { background: 'transparent' },
                      rail: {
                        background: `linear-gradient(to right, #60a5fa 0%, #60a5fa ${splitPos}%, #f472b6 ${splitPos}%, #f472b6 100%)`,
                      },
                    }}
                    tooltip={{
                      formatter: (v) => (
                        <div>
                          <div>{t('kb.recallVector')}: {(v ?? 0).toFixed(2)}</div>
                          <div>{t('kb.recallKeyword')}: {(1 - (v ?? 0)).toFixed(2)}</div>
                        </div>
                      ),
                    }}
                  />
                  <div className="appearance-slider-hint">
                    <span>{t('kb.ratioVector', { percent: vectorPct })}</span>
                    <span>{t('kb.ratioKeyword', { percent: keywordPct })}</span>
                  </div>
                </div>

                {/* Authorized access: pills; edit mode makes them checkable */}
                <div className="kb-section">
                  <div className="kb-section-header">
                    <Tooltip title={t('kb.accessHint')}>
                      <div className="kb-section-title">{t('kb.accessTitle')}</div>
                    </Tooltip>
                    {editingAccess ? (
                      <div className="kb-section-header-actions">
                        <Tooltip title={t('common.cancel')}>
                          <Button type="text" size="small" icon={<CloseOutlined />} onClick={handleCancelAccessEdit} disabled={savingAccess} />
                        </Tooltip>
                        <Tooltip title={t('kb.accessDone')}>
                          <Button type="text" size="small" icon={<CheckOutlined />} onClick={handleCommitAccessEdit} loading={savingAccess} />
                        </Tooltip>
                      </div>
                    ) : (
                      <Tooltip title={t('common.edit')}>
                        <Button type="text" size="small" icon={<EditOutlined />} onClick={handleStartAccessEdit} />
                      </Tooltip>
                    )}
                  </div>
                  {!editingAccess ? (
                    accessList.length > 0 ? (
                      <div className="kb-access-list">
                        {accessList.map(person => (
                          <div key={person.id} className="kb-access-item">
                            <AgentAvatar avatar={person.avatar} size={20} iconSize={10} borderRadius="50%" />
                            <span>{person.name}</span>
                          </div>
                        ))}
                      </div>
                    ) : (
                      <div className="kb-access-empty">{t('kb.accessEmpty')}</div>
                    )
                  ) : (
                    <div className="kb-access-list">
                      {agents.length > 0 ? agents.map(a => {
                        const checked = draftIds.has(a.id);
                        return (
                          <div
                            key={a.id}
                            className={`kb-access-item kb-access-checkable${checked ? ' checked' : ''}`}
                            onClick={() => handleToggleDraft(a.id)}
                          >
                            <AgentAvatar avatar={a.avatar} size={20} iconSize={10} borderRadius="50%" />
                            <span>{a.name}</span>
                            {checked && <CheckOutlined className="kb-access-check" />}
                          </div>
                        );
                      }) : (
                        <div className="kb-access-empty">{t('kb.accessEmpty')}</div>
                      )}
                    </div>
                  )}
                </div>
              </>
            ),
          },
        ]}
      />
    </div>
  );
};

export default KnowledgeBaseDetail;
