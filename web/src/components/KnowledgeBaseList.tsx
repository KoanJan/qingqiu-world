import React, { useEffect, useState } from 'react';
import { Modal, Form, Input, message, Tag } from 'antd';
import { useTranslation } from 'react-i18next';
import CardActions from './CardActions';
import type { KnowledgeBase } from '../types';
import { kbApi } from '../services/api';
import { logger } from '../logger';
import { confirmDelete } from '../utils/confirm';
import ConfigIcon from './ConfigIcon';

interface KnowledgeBaseListProps {
  onSelectKB?: (kb: KnowledgeBase) => void;
  showCreate?: boolean;
  onCreateClose?: () => void;
}

const STATUS_MAP: Record<number, { color: string; labelKey: string }> = {
  0: { color: 'default', labelKey: 'kb.indexTypeFlat' },
  1: { color: 'processing', labelKey: 'kb.indexTypeSwitching' },
  2: { color: 'success', labelKey: 'kb.indexTypeHNSW' },
};

const KnowledgeBaseList: React.FC<KnowledgeBaseListProps> = ({ onSelectKB, showCreate, onCreateClose }) => {
  const { t } = useTranslation();
  const [kbs, setKBs] = useState<KnowledgeBase[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalVisible, setModalVisible] = useState(false);
  const [editModalVisible, setEditModalVisible] = useState(false);
  const [editingKB, setEditingKB] = useState<KnowledgeBase | null>(null);
  const [form] = Form.useForm();
  const [editForm] = Form.useForm();

  const loadKBs = async () => {
    setLoading(true);
    try {
      const response = await kbApi.list();
      setKBs(response.data);
    } catch (error) {
      message.error(t('messages.loadFailed'));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadKBs();
  }, []);

  useEffect(() => {
    if (showCreate) {
      setModalVisible(true);
    }
  }, [showCreate]);

  const handleModalClose = () => {
    setModalVisible(false);
    form.resetFields();
    onCreateClose?.();
  };

  const handleCreate = async (values: Record<string, unknown>) => {
    try {
      const response = await kbApi.create(values as Partial<KnowledgeBase>);
      setKBs([response.data, ...kbs]);
      setModalVisible(false);
      form.resetFields();
      message.success(t('kb.createSuccess'));
    } catch (error) {
      logger.error('Failed to create knowledge base:', error);
      message.error(t('kb.createFailed'));
    }
  };

  const handleUpdate = async (values: Record<string, unknown>) => {
    if (!editingKB) return;
    try {
      const response = await kbApi.update(editingKB.id, values as Partial<KnowledgeBase>);
      const index = kbs.findIndex(kb => kb.id === editingKB.id);
      if (index !== -1) {
        const newKBs = [...kbs];
        newKBs[index] = response.data;
        setKBs(newKBs);
      }
      setEditModalVisible(false);
      editForm.resetFields();
      setEditingKB(null);
      message.success(t('kb.updateSuccess'));
    } catch (error) {
      logger.error('Failed to update knowledge base:', error);
      message.error(t('kb.updateFailed'));
    }
  };

  const handleDelete = async (kbId: number, e: React.MouseEvent) => {
    e.stopPropagation();
    confirmDelete({
      title: t('kb.confirmDeleteTitle'),
      content: t('kb.confirmDelete'),
      okText: t('common.delete'),
      cancelText: t('common.cancel'),
      onOk: async () => {
        try {
          await kbApi.delete(kbId);
          setKBs(kbs.filter(kb => kb.id !== kbId));
          message.success(t('kb.deleteSuccess'));
        } catch (error) {
          logger.error('Failed to delete knowledge base:', error);
          message.error(t('kb.deleteFailed'));
        }
      },
    });
  };

  const handleEdit = (kb: KnowledgeBase) => {
    setEditingKB(kb);
    editForm.setFieldsValue({
      name: kb.name,
      description: kb.description,
    });
    setEditModalVisible(true);
  };

  const renderForm = (isEdit: boolean) => {
    const currentForm = isEdit ? editForm : form;
    const currentKB = isEdit ? editingKB : null;
    return (
      <Form form={currentForm} layout="vertical" onFinish={isEdit ? handleUpdate : handleCreate} style={{ marginTop: '16px' }}>
        <Form.Item
          label={t('kb.name')}
          name="name"
          rules={[{ required: true, message: t('kb.namePlaceholder') }]}
        >
          <Input placeholder={t('kb.namePlaceholder')} />
        </Form.Item>
        <Form.Item label={t('kb.description')} name="description">
          <Input.TextArea placeholder={t('kb.descriptionPlaceholder')} rows={2} />
        </Form.Item>
        {isEdit && currentKB && (
          <div style={{ marginBottom: 16 }}>
            <div style={{ fontSize: 13, color: 'var(--color-text-secondary)', marginBottom: 4 }}>
              {t('kb.indexType')}
            </div>
            <Tag color={STATUS_MAP[currentKB.index_type]?.color || 'default'}>
              {t(STATUS_MAP[currentKB.index_type]?.labelKey || String(currentKB.index_type))}
            </Tag>
            <span style={{ marginLeft: 12, fontSize: 13, color: 'var(--color-text-secondary)' }}>
              {t('kb.docCount', { count: currentKB.document_count })} · {t('kb.vectorCount', { count: currentKB.vector_count })}
            </span>
          </div>
        )}
      </Form>
    );
  };

  return (
    <>
      <div>
        {loading ? (
          <div className="empty-state-text">{t('sidebar.loading')}</div>
        ) : kbs.length === 0 ? (
          <div className="empty-state-text">{t('kb.noKB')}</div>
        ) : (
          kbs.map(kb => (
            <div
              key={kb.id}
              className="item-card"
              style={{ cursor: onSelectKB ? 'pointer' : 'default' }}
              onClick={() => onSelectKB?.(kb)}
            >
              <div className="item-card-header">
                <ConfigIcon type="kb" />
                <div className="item-card-info">
                  <div className="item-card-name">{kb.name}</div>
                  <div className="item-card-desc">
                    {kb.description || t('kb.noDescription')}
                    <span style={{ marginLeft: 8 }}>
                      <Tag color={STATUS_MAP[kb.index_type]?.color || 'default'} style={{ fontSize: 11 }}>
                        {t(STATUS_MAP[kb.index_type]?.labelKey || String(kb.index_type))}
                      </Tag>
                      {t('kb.docCount', { count: kb.document_count })} · {t('kb.vectorCount', { count: kb.vector_count })}
                    </span>
                  </div>
                </div>
                <CardActions
                  onEdit={(e) => { e.stopPropagation(); handleEdit(kb); }}
                  onDelete={(e) => handleDelete(kb.id, e)}
                />
              </div>
            </div>
          ))
        )}
      </div>

      <Modal
        title={t('kb.create')}
        open={modalVisible}
        onOk={() => form.submit()}
        onCancel={handleModalClose}
        okText={t('common.create')}
        cancelText={t('common.cancel')}
      >
        {renderForm(false)}
      </Modal>

      <Modal
        title={t('kb.edit')}
        open={editModalVisible}
        onOk={() => editForm.submit()}
        onCancel={() => { setEditModalVisible(false); editForm.resetFields(); setEditingKB(null); }}
        okText={t('common.update')}
        cancelText={t('common.cancel')}
      >
        {renderForm(true)}
      </Modal>
    </>
  );
};

export default KnowledgeBaseList;
