import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Button, Modal, Form, Upload, message, Spin, Tag } from 'antd';
import { UploadOutlined, ReloadOutlined } from '@ant-design/icons';
import type { UploadFile } from 'antd';
import { useTranslation } from 'react-i18next';
import CardActions from './CardActions';
import { confirmDelete } from '../utils/confirm';
import { experienceSourceLabel, experienceStatusInfo, experienceDisplayTitle } from '../utils/experience';
import type { PublicExperience } from '../types';
import {
  PUBLIC_EXPERIENCE_STATUS_GENERATING,
  PUBLIC_EXPERIENCE_STATUS_ERROR,
} from '../types';
import { publicExperienceApi } from '../services/api';
import { subscribeClientNotifications, CLIENT_NOTIFICATION_TYPES } from '../services/clientNotifications';

interface PublicExperienceListProps {
  active: boolean;
  showIngest?: boolean;
  onIngestClose?: () => void;
  onSelectExp?: (exp: PublicExperience) => void;
}

const FALLBACK_INTERVAL_MS = 5 * 60 * 1000;

const PublicExperienceList: React.FC<PublicExperienceListProps> = ({ active, showIngest, onIngestClose, onSelectExp }) => {
  const { t } = useTranslation();
  const [experiences, setExperiences] = useState<PublicExperience[]>([]);
  const [loading, setLoading] = useState(false);
  const [ingestVisible, setIngestVisible] = useState(false);
  const [ingesting, setIngesting] = useState(false);
  const [_redistillingId, setRedistillingId] = useState<number | null>(null);
  const [ingestForm] = Form.useForm();
  const [fileContent, setFileContent] = useState<string | null>(null);
  const [fileName, setFileName] = useState<string | null>(null);
  const latestEventIdRef = useRef(0);
  const dirtyRef = useRef(false);
  const requestRef = useRef(0);
  const loadedRef = useRef(false);

  // Load the experience list. When silent=true, no loading spinner / error toast
  // is shown — used by the polling loop to avoid disruptive UX.
  const loadExperiences = useCallback(async (silent = false) => {
    const requestID = ++requestRef.current;
    if (!silent) setLoading(true);
    try {
      const res = await publicExperienceApi.list();
      if (requestID === requestRef.current) setExperiences(res.data);
    } catch {
      if (!silent) message.error(t('publicExperience.loadError'));
    } finally {
      if (!silent) setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    return subscribeClientNotifications((notification) => {
      if (notification.type === CLIENT_NOTIFICATION_TYPES.STREAM_RECONNECTED) {
        latestEventIdRef.current = 0;
        dirtyRef.current = true;
        if (active) {
          dirtyRef.current = false;
          void loadExperiences(true);
        }
        return;
      }
      if (notification.type !== CLIENT_NOTIFICATION_TYPES.PUBLIC_EXPERIENCE_UPDATED && notification.type !== CLIENT_NOTIFICATION_TYPES.PUBLIC_EXPERIENCE_DELETED) return;
      if (typeof notification.id === 'number') {
        if (notification.id <= latestEventIdRef.current) return;
        latestEventIdRef.current = notification.id;
      }
      dirtyRef.current = true;
      if (active) {
        dirtyRef.current = false;
        void loadExperiences(true);
      }
    });
  }, [active, loadExperiences]);

  useEffect(() => {
    if (!active) return;
    if (!loadedRef.current || dirtyRef.current) {
      loadedRef.current = true;
      dirtyRef.current = false;
      void loadExperiences(true);
    }
    const timer = window.setInterval(() => void loadExperiences(true), FALLBACK_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [active, loadExperiences]);

  useEffect(() => {
    if (showIngest) {
      setIngestVisible(true);
    }
  }, [showIngest]);

  const closeIngest = () => {
    setIngestVisible(false);
    ingestForm.resetFields();
    setFileContent(null);
    setFileName(null);
    onIngestClose?.();
  };

  const handleDelete = async (id: number) => {
    confirmDelete({
      title: t('publicExperience.confirmDeleteTitle'),
      content: t('publicExperience.confirmDelete'),
      okText: t('common.delete'),
      cancelText: t('common.cancel'),
      onOk: async () => {
        try {
          await publicExperienceApi.delete(id);
          setExperiences(prev => prev.filter(e => e.id !== id));
          message.success(t('publicExperience.deleteSuccess'));
        } catch {
          message.error(t('publicExperience.deleteFailed'));
        }
      },
    });
  };

  const handleFileChange = (info: { fileList: UploadFile[] }) => {
    const file = info.fileList[0]?.originFileObj;
    if (!file) {
      setFileContent(null);
      setFileName(null);
      return;
    }
    const reader = new FileReader();
    reader.onload = () => {
      setFileName(file.name);
      setFileContent(reader.result as string);
    };
    reader.onerror = () => {
      message.error(t('publicExperience.readFileError'));
    };
    reader.readAsText(file);
  };

  const handleIngest = async () => {
    if (!fileContent || !fileName) return;

    setIngesting(true);
    try {
      await publicExperienceApi.ingest({
        file_name: fileName,
        raw_content: fileContent,
      });
      closeIngest();
      message.success(t('publicExperience.ingestSuccess'));
      // Refresh list so the newly pre-written Generating record shows up.
      loadExperiences();
    } catch {
      message.error(t('publicExperience.ingestFailed'));
    } finally {
      setIngesting(false);
    }
  };

  const handleRedistill = async (e: React.MouseEvent, id: number) => {
    e.stopPropagation();
    setRedistillingId(id);
    try {
      await publicExperienceApi.redistill(id);
      message.success(t('publicExperience.redistillSuccess'));
      loadExperiences();
    } catch {
      message.error(t('publicExperience.redistillFailed'));
    } finally {
      setRedistillingId(null);
    }
  };

  const sourceLabel = (sourceType: number) =>
    experienceSourceLabel(sourceType, t);

  // Render a status tag for non-Active experiences. Active records show no tag
  // (the normal case) to avoid visual noise.
  const renderStatusTag = (status: number) => {
    const info = experienceStatusInfo(status, t);
    if (!info) return null;
    return <Tag color={info.color}>{info.label}</Tag>;
  };

  // Dynamic title: for non-Active statuses, prepend/append status text around
  // the placeholder title (derived from the uploaded skill). For Active, show
  // the LLM-generated title as-is.
  const displayTitle = (exp: PublicExperience): string =>
    experienceDisplayTitle(exp, t);

  return (
    <>
      <div>
        {loading ? (
          <div style={{ display: 'flex', justifyContent: 'center', padding: '40px' }}>
            <Spin />
          </div>
        ) : experiences.length === 0 ? (
          <div className="empty-state-text">{t('publicExperience.noData')}</div>
        ) : (
          <div className="list-grid-2">
            {experiences.map(exp => {
              const isGenerating = exp.status === PUBLIC_EXPERIENCE_STATUS_GENERATING;
              const isError = exp.status === PUBLIC_EXPERIENCE_STATUS_ERROR;
              return (
                <div
                  key={exp.id}
                  className="item-card"
                  style={{ cursor: isGenerating ? 'default' : 'pointer', opacity: isGenerating ? 0.6 : 1 }}
                  onClick={() => !isGenerating && onSelectExp?.(exp)}
                >
                  <div className="item-card-header">
                    <div className="item-card-info" style={{ flex: 1 }}>
                      <div className="item-card-name">
                        {displayTitle(exp)}
                      </div>
                      <div className="item-card-desc">
                        {exp.description}
                      </div>
                      <div className="item-card-desc" style={{ fontSize: '11px', opacity: 0.6, display: 'flex', alignItems: 'center', gap: 6 }}>
                        {renderStatusTag(exp.status)}
                        <span>{sourceLabel(exp.source_type)} · {new Date(exp.created_at).toLocaleDateString()}</span>
                      </div>
                    </div>
                  </div>
                  <CardActions
                    onDelete={(e) => { e.stopPropagation(); handleDelete(exp.id); }}
                  >
                    {isError && (
                      <button
                        className="item-card-action-btn"
                        onClick={(e) => handleRedistill(e, exp.id)}
                      >
                        <ReloadOutlined />
                      </button>
                    )}
                  </CardActions>
                </div>
              );
            })}
          </div>
        )}
      </div>

      <Modal
        title={t('publicExperience.ingest')}
        open={ingestVisible}
        width={700}
        onCancel={closeIngest}
        onOk={handleIngest}
        confirmLoading={ingesting}
        okText={t('common.confirm')}
        cancelText={t('common.cancel')}
      >
        <Form
          form={ingestForm}
          layout="vertical"
          style={{ marginTop: '16px' }}
        >
          <Form.Item
            name="file"
            label={t('publicExperience.skillFile')}
            rules={[{ required: true, message: t('publicExperience.skillFileRequired') }]}
            valuePropName="fileList"
            getValueFromEvent={e => e?.fileList}
          >
            <Upload
              accept=".md"
              maxCount={1}
              beforeUpload={() => false}
              onChange={handleFileChange}
            >
              <Button icon={<UploadOutlined />}>{t('publicExperience.uploadHint')}</Button>
            </Upload>
          </Form.Item>

          {fileContent && (
            <Form.Item label={t('publicExperience.preview')}>
              <div style={{
                maxHeight: 300,
                overflow: 'auto',
                border: '1px solid var(--color-border)',
                borderRadius: 6,
                padding: '12px 16px',
                background: 'var(--color-bg-secondary)',
              }}>
                <pre style={{
                  margin: 0,
                  fontFamily: 'monospace',
                  fontSize: 12,
                  lineHeight: 1.5,
                  whiteSpace: 'pre-wrap',
                  wordBreak: 'break-word',
                }}>
                  {fileContent}
                </pre>
              </div>
            </Form.Item>
          )}
        </Form>
      </Modal>
    </>
  );
};

export default PublicExperienceList;
