import React, { useEffect, useState } from 'react';
import { Form, Input, Modal, Select, Upload, message } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import AgentAvatar from './AgentAvatar';
import CardActions from './CardActions';
import type { Agent, LLMConfig } from '../types';
import { agentApi, llmConfigApi, uploadApi } from '../services/api';
import { logger } from '../logger';
import { confirmDelete } from '../utils/confirm';

interface AgentConfigProps {
  showCreate?: boolean;
  onCreateClose?: () => void;
  onAgentCreated?: () => void;
  onSelectAgent: (agent: Agent) => void;
}

// AgentConfig is deliberately a list and creation entry only. The complete
// configuration of an existing Agent belongs to the dedicated AgentDetail page.
const AgentConfig: React.FC<AgentConfigProps> = ({ showCreate, onCreateClose, onAgentCreated, onSelectAgent }) => {
  const { t } = useTranslation();
  const [agents, setAgents] = useState<Agent[]>([]);
  const [llmConfigs, setLLMConfigs] = useState<LLMConfig[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalVisible, setModalVisible] = useState(false);
  const [form] = Form.useForm();
  const [createAvatarFile, setCreateAvatarFile] = useState<File | null>(null);
  const [createAvatarPreview, setCreateAvatarPreview] = useState('');

  useEffect(() => () => {
    if (createAvatarPreview) URL.revokeObjectURL(createAvatarPreview);
  }, [createAvatarPreview]);

  const loadAgents = async () => {
    setLoading(true);
    try {
      const response = await agentApi.list();
      setAgents(response.data);
    } catch (error) {
      logger.error('Failed to load agents', error);
      message.error(t('messages.loadFailed'));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void loadAgents();
    llmConfigApi.list().then(response => setLLMConfigs(response.data)).catch(error => {
      logger.error('Failed to load LLM configs', error);
    });
  }, []);

  useEffect(() => {
    if (showCreate) setModalVisible(true);
  }, [showCreate]);

  const closeCreate = () => {
    setModalVisible(false);
    form.resetFields();
    setCreateAvatarFile(null);
    setCreateAvatarPreview('');
    onCreateClose?.();
  };

  const handleCreate = async (values: Record<string, unknown>) => {
    try {
      let avatar = '';
      if (createAvatarFile) {
        avatar = (await uploadApi.uploadAvatar(createAvatarFile)).data.filename;
      }
      const agent = (await agentApi.create({ ...values, avatar })).data;
      setAgents(previous => [agent, ...previous]);
      closeCreate();
      message.success(t('agent.createSuccess'));
      onAgentCreated?.();
      // Continue directly to the full configuration page after the minimal create.
      onSelectAgent(agent);
    } catch (error) {
      logger.error('Failed to create agent', error);
      message.error(t('agent.createFailed'));
    }
  };

  const handleDelete = (agent: Agent, event: React.MouseEvent) => {
    event.stopPropagation();
    confirmDelete({
      title: t('agent.confirmDeleteTitle'),
      content: t('agent.confirmDelete'),
      okText: t('common.delete'),
      cancelText: t('common.cancel'),
      onOk: async () => {
        try {
          await agentApi.delete(agent.id);
          setAgents(previous => previous.filter(item => item.id !== agent.id));
          message.success(t('agent.deleteSuccess'));
        } catch (error) {
          logger.error('Failed to delete agent', error);
          message.error(t('agent.deleteFailed'));
        }
      },
    });
  };

  const renderAvatarUpload = () => (
    <Upload
      accept=".jpg,.jpeg,.png,.webp"
      showUploadList={false}
      beforeUpload={(file) => {
        if (createAvatarPreview) URL.revokeObjectURL(createAvatarPreview);
        setCreateAvatarFile(file);
        setCreateAvatarPreview(URL.createObjectURL(file));
        return false;
      }}
    >
      {createAvatarPreview ? (
        <div className="avatar-upload-preview">
          <img src={createAvatarPreview} alt="preview" className="avatar-upload-preview-img" />
          <div className="avatar-upload-overlay"><PlusOutlined /></div>
        </div>
      ) : (
        <div className="avatar-upload-trigger">
          <PlusOutlined style={{ fontSize: '20px', color: 'var(--color-text-placeholder)' }} />
          <div style={{ marginTop: '4px', fontSize: '12px', color: 'var(--color-text-placeholder)' }}>{t('agent.avatarUpload')}</div>
        </div>
      )}
    </Upload>
  );

  return (
    <>
      <div className="item-card-grid">
        {loading ? (
          <div className="empty-state-text">{t('sidebar.loading')}</div>
        ) : agents.length === 0 ? (
          <div className="empty-state-text">{t('sidebar.noAgent')}</div>
        ) : (
          agents.map(agent => (
            <div className="item-card item-card-block" key={agent.id}>
              <AgentAvatar avatar={agent.avatar} size={44} iconSize={20} borderRadius="10px" />
              <div className="item-card-block-name">{agent.name}</div>
              <CardActions
                onEdit={(event) => { event.stopPropagation(); onSelectAgent(agent); }}
                onDelete={(event) => handleDelete(agent, event)}
              />
            </div>
          ))
        )}
      </div>

      <Modal title={t('agent.create')} open={modalVisible} onOk={() => form.submit()} onCancel={closeCreate} okText={t('common.create')} cancelText={t('common.cancel')} width={600}>
        <Form form={form} layout="vertical" onFinish={handleCreate} style={{ marginTop: '16px' }}>
          <Form.Item label={t('agent.avatar')}>{renderAvatarUpload()}</Form.Item>
          <Form.Item name="name" label={t('agent.name')} rules={[{ required: true, message: t('agent.namePlaceholder') }]} extra={<span style={{ fontSize: 12, color: 'var(--color-text-placeholder)' }}>{t('userProfile.nameImmutable')}</span>}>
            <Input placeholder={t('agent.namePlaceholder')} />
          </Form.Item>
          <Form.Item name="character_settings" label={t('agent.characterSettings')}>
            <Input.TextArea rows={4} placeholder={t('agent.characterSettingsPlaceholder')} />
          </Form.Item>
          <Form.Item name="llm_config_id" label={t('agent.llmConfigId')} rules={[{ required: true, message: t('agent.llmConfigIdPlaceholder') }]}>
            <Select placeholder={t('agent.llmConfigIdPlaceholder')} options={llmConfigs.map(config => ({ value: config.id, label: config.name }))} />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
};

export default AgentConfig;
