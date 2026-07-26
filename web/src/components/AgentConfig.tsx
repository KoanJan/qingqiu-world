import React, { useEffect, useState } from 'react';
import { Modal, Form, Input, message, Select, Upload } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import AgentAvatar from './AgentAvatar';
import CardActions from './CardActions';
import type { Agent, LLMConfig, KnowledgeBase } from '../types';
import { agentApi, llmConfigApi, kbApi, uploadApi } from '../services/api';
import { logger } from '../logger';
import { confirmDelete } from '../utils/confirm';

interface AgentConfigProps {
  showCreate?: boolean;
  onCreateClose?: () => void;
  onAgentCreated?: () => void;
}

const AgentConfig: React.FC<AgentConfigProps> = ({ showCreate, onCreateClose, onAgentCreated }) => {
  const { t } = useTranslation();
  const [agents, setAgents] = useState<Agent[]>([]);
  const [llmConfigs, setLLMConfigs] = useState<LLMConfig[]>([]);
  const [knowledgeBases, setKnowledgeBases] = useState<KnowledgeBase[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalVisible, setModalVisible] = useState(false);
  const [editModalVisible, setEditModalVisible] = useState(false);
  const [editingAgent, setEditingAgent] = useState<Agent | null>(null);
  const [form] = Form.useForm();
  const [editForm] = Form.useForm();
  const [createAvatarFile, setCreateAvatarFile] = useState<File | null>(null);
  const [createAvatarPreview, setCreateAvatarPreview] = useState<string>('');
  const [editAvatarFile, setEditAvatarFile] = useState<File | null>(null);
  const [editAvatarPreview, setEditAvatarPreview] = useState<string>('');

  // Revoke object URLs on change to prevent memory leaks
  useEffect(() => () => {
    if (createAvatarPreview) URL.revokeObjectURL(createAvatarPreview);
  }, [createAvatarPreview]);

  useEffect(() => () => {
    if (editAvatarPreview) URL.revokeObjectURL(editAvatarPreview);
  }, [editAvatarPreview]);

  const loadAgents = async () => {
    setLoading(true);
    try {
      const response = await agentApi.list();
      setAgents(response.data);
    } catch (error) {
      message.error(t('messages.loadFailed'));
    } finally {
      setLoading(false);
    }
  };

  const loadLLMConfigs = async () => {
    try {
      const response = await llmConfigApi.list();
      setLLMConfigs(response.data);
    } catch (error) {
      logger.error('Failed to load LLM configs:', error);
    }
  };

  const loadKnowledgeBases = async () => {
    try {
      const response = await kbApi.list();
      setKnowledgeBases(response.data);
    } catch (error) {
      logger.error('Failed to load knowledge bases:', error);
    }
  };

  useEffect(() => {
    loadAgents();
    loadLLMConfigs();
    loadKnowledgeBases();
  }, []);

  useEffect(() => {
    if (showCreate) {
      setModalVisible(true);
    }
  }, [showCreate]);

  const handleModalClose = () => {
    setModalVisible(false);
    form.resetFields();
    setCreateAvatarFile(null);
    setCreateAvatarPreview('');
    if (onCreateClose) {
      onCreateClose();
    }
  };

  const handleCreateAgent = async (values: Record<string, unknown>) => {
    try {
      let avatarFilename = '';
      if (createAvatarFile) {
        try {
          const uploadRes = await uploadApi.uploadAvatar(createAvatarFile);
          avatarFilename = uploadRes.data.filename;
        } catch (error) {
          logger.error('Failed to upload avatar:', error);
        }
      }

      const response = await agentApi.create({ ...values, avatar: avatarFilename });
      const newAgent = response.data;

      setAgents([newAgent, ...agents]);
      setModalVisible(false);
      form.resetFields();
      onCreateClose?.();
      setCreateAvatarFile(null);
      setCreateAvatarPreview('');
      message.success(t('agent.createSuccess'));
      if (onAgentCreated) {
        onAgentCreated();
      }
    } catch (error) {
      logger.error('Failed to create agent:', error);
      message.error(t('agent.createFailed'));
    }
  };

  const handleUpdateAgent = async (values: Record<string, unknown>) => {
    if (!editingAgent) return;

    try {
      let avatarFilename = editingAgent.avatar;

      if (editAvatarFile) {
        try {
          const uploadRes = await uploadApi.uploadAvatar(editAvatarFile);
          avatarFilename = uploadRes.data.filename;
        } catch (error) {
          logger.error('Failed to upload avatar:', error);
        }
      }

      const updateData = { ...values, avatar: avatarFilename };
      const response = await agentApi.update(editingAgent.id, updateData);
      const index = agents.findIndex(a => a.id === editingAgent.id);
      if (index !== -1) {
        const newAgents = [...agents];
        newAgents[index] = response.data;
        setAgents(newAgents);
      }
      setEditModalVisible(false);
      editForm.resetFields();
      setEditingAgent(null);
      setEditAvatarFile(null);
      setEditAvatarPreview('');
      message.success(t('agent.updateSuccess'));
    } catch (error) {
      logger.error('Failed to update agent:', error);
      message.error(t('agent.updateFailed'));
    }
  };

  const handleDeleteAgent = async (agentId: number, e: React.MouseEvent) => {
    e.stopPropagation();

    confirmDelete({
      title: t('agent.confirmDeleteTitle'),
      content: t('agent.confirmDelete'),
      okText: t('common.delete'),
      cancelText: t('common.cancel'),
      onOk: async () => {
        try {
          await agentApi.delete(agentId);
          setAgents(agents.filter(a => a.id !== agentId));
          message.success(t('agent.deleteSuccess'));
        } catch (error) {
          logger.error('Failed to delete agent:', error);
          message.error(t('agent.deleteFailed'));
        }
      },
    });
  };

  const handleEditAgent = (agent: Agent) => {
    setEditingAgent(agent);
    setEditAvatarFile(null);
    setEditAvatarPreview('');
    editForm.setFieldsValue({
      character_settings: agent.character_settings || '',
      llm_config_id: agent.llm_config_id,
      knowledge_base_ids: agent.knowledge_base_ids || [],
    });
    setEditModalVisible(true);
  };

  const renderAvatarUpload = (
    setAvatarFile: (f: File | null) => void,
    setAvatarPreview: (url: string) => void,
    currentAvatar?: string,
    previewUrl?: string,
  ) => {
    const showImage = previewUrl || currentAvatar;

    return (
      <Upload
        accept=".jpg,.jpeg,.png,.webp"
        showUploadList={false}
        beforeUpload={(file) => {
          setAvatarFile(file);
          setAvatarPreview(URL.createObjectURL(file));
          return false;
        }}
      >
        {showImage ? (
          <div className="avatar-upload-preview">
            {previewUrl ? (
              <img
                src={previewUrl}
                alt="preview"
                className="avatar-upload-preview-img"
              />
            ) : (
              <AgentAvatar
                avatar={currentAvatar || ''}
                size={64}
                borderRadius="50%"
                iconSize={28}
              />
            )}
            <div className="avatar-upload-overlay">
              <PlusOutlined />
            </div>
          </div>
        ) : (
          <div className="avatar-upload-trigger">
            <PlusOutlined style={{ fontSize: '20px', color: 'var(--color-text-placeholder)' }} />
            <div style={{ marginTop: '4px', fontSize: '12px', color: 'var(--color-text-placeholder)' }}>
              {t('agent.avatarUpload')}
            </div>
          </div>
        )}
      </Upload>
    );
  };

  return (
    <>
      <div className="item-card-grid">
        {loading ? (
          <div className="empty-state-text">
            {t('sidebar.loading')}
          </div>
        ) : agents.length === 0 ? (
          <div className="empty-state-text">
            {t('sidebar.noAgent')}
          </div>
        ) : (
          agents.map((agent) => (
            <div
              key={agent.id}
              className="item-card item-card-block"
            >
              <AgentAvatar avatar={agent.avatar} size={44} iconSize={20} borderRadius="10px" />
              <div className="item-card-block-name">{agent.name}</div>
              <CardActions
                onEdit={(e) => { e.stopPropagation(); handleEditAgent(agent); }}
                onDelete={(e) => handleDeleteAgent(agent.id, e)}
              />
            </div>
          ))
        )}
      </div>

      <Modal
        title={t('agent.create')}
        open={modalVisible}
        onOk={() => form.submit()}
        onCancel={handleModalClose}
        okText={t('common.create')}
        cancelText={t('common.cancel')}
        width={600}
      >
        <Form
          form={form}
          layout="vertical"
          name="agent_form"
          onFinish={handleCreateAgent}
          style={{ marginTop: '16px' }}
        >
          <Form.Item label={t('agent.avatar')}>
            {renderAvatarUpload(setCreateAvatarFile, setCreateAvatarPreview, undefined, createAvatarPreview)}
          </Form.Item>

          <Form.Item
            label={t('agent.name')}
            name="name"
            rules={[{ required: true, message: t('agent.namePlaceholder') }]}
            extra={<span style={{ fontSize: 12, color: 'var(--color-text-placeholder)' }}>{t('userProfile.nameImmutable')}</span>}
          >
            <Input placeholder={t('agent.namePlaceholder')} />
          </Form.Item>

          <Form.Item
            label={t('agent.characterSettings')}
            name="character_settings"
          >
            <Input.TextArea
              placeholder={t('agent.characterSettingsPlaceholder')}
              rows={4}
            />
          </Form.Item>

          <Form.Item
            label={t('agent.llmConfigId')}
            name="llm_config_id"
            rules={[{ required: true, message: t('agent.llmConfigIdPlaceholder') }]}
          >
            <Select placeholder={t('agent.llmConfigIdPlaceholder')}>
              {llmConfigs.map(config => (
                <Select.Option key={config.id} value={config.id}>
                  {config.name}
                </Select.Option>
              ))}
            </Select>
          </Form.Item>

          <Form.Item
            label={t('agent.knowledgeBaseIds')}
            name="knowledge_base_ids"
          >
            <Select
              mode="multiple"
              placeholder={t('agent.knowledgeBaseIdsPlaceholder')}
              allowClear
            >
              {knowledgeBases.map(kb => (
                <Select.Option key={kb.id} value={kb.id}>
                  {kb.name}
                </Select.Option>
              ))}
            </Select>
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={t('agent.edit')}
        open={editModalVisible}
        onOk={() => editForm.submit()}
        onCancel={() => {
          setEditModalVisible(false);
          editForm.resetFields();
          setEditingAgent(null);
          setEditAvatarFile(null);
          setEditAvatarPreview('');
        }}
        okText={t('common.update')}
        cancelText={t('common.cancel')}
        width={600}
      >
        <Form
          form={editForm}
          layout="vertical"
          name="agent_edit_form"
          onFinish={handleUpdateAgent}
          style={{ marginTop: '16px' }}
        >
          <Form.Item label={t('agent.avatar')}>
            {renderAvatarUpload(
              setEditAvatarFile,
              setEditAvatarPreview,
              editingAgent?.avatar,
              editAvatarPreview,
            )}
          </Form.Item>

          <div style={{ marginBottom: 24, marginTop: -8 }}>
            <div style={{ fontSize: 14, color: 'var(--color-text-secondary)', marginBottom: 4 }}>
              {t('agent.name')}
            </div>
            <div style={{ fontSize: 15, fontWeight: 500 }}>
              {editingAgent?.name}
            </div>
            <div style={{ fontSize: 12, color: 'var(--color-text-placeholder)', marginTop: 2 }}>
              {t('userProfile.nameImmutable')}
            </div>
          </div>

          <Form.Item
            label={t('agent.characterSettings')}
            name="character_settings"
          >
            <Input.TextArea
              placeholder={t('agent.characterSettingsPlaceholder')}
              rows={4}
            />
          </Form.Item>

          <Form.Item
            label={t('agent.llmConfigId')}
            name="llm_config_id"
            rules={[{ required: true, message: t('agent.llmConfigIdPlaceholder') }]}
          >
            <Select placeholder={t('agent.llmConfigIdPlaceholder')}>
              {llmConfigs.map(config => (
                <Select.Option key={config.id} value={config.id}>
                  {config.name}
                </Select.Option>
              ))}
            </Select>
          </Form.Item>

          <Form.Item
            label={t('agent.knowledgeBaseIds')}
            name="knowledge_base_ids"
          >
            <Select
              mode="multiple"
              placeholder={t('agent.knowledgeBaseIdsPlaceholder')}
              allowClear
            >
              {knowledgeBases.map(kb => (
                <Select.Option key={kb.id} value={kb.id}>
                  {kb.name}
                </Select.Option>
              ))}
            </Select>
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
};

export default AgentConfig;
