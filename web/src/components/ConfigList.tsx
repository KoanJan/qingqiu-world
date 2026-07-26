import React, { useEffect, useState } from 'react';
import { Modal, Input, message, Form } from 'antd';
import { useTranslation } from 'react-i18next';
import CardActions from './CardActions';
import { logger } from '../logger';
import { confirmDelete } from '../utils/confirm';

interface FormField {
  name: string;
  labelKey: string;
  placeholderKey: string;
  required?: boolean;
  type?: 'input' | 'password' | 'textarea';
  rows?: number;
}

interface ConfigListProps<T extends { id: number }> {
  api: {
    list: () => Promise<{ data: T[] }>;
    create: (data: Record<string, unknown>) => Promise<{ data: T }>;
    update: (id: number, data: Record<string, unknown>) => Promise<{ data: T }>;
    delete: (id: number) => Promise<unknown>;
  };
  formFields: FormField[];
  i18nPrefix: string;
  onSelectConfig?: (config: T | null) => void;
  showCreate?: boolean;
  onCreateClose?: () => void;
  onConfigChanged?: () => void;
  beforeDelete?: (id: number) => Promise<boolean>;
  // Field rendered as the primary (top, bold) line of each card.
  primaryField: string;
  // Field rendered as the secondary (bottom, muted) line of each card. A native
  // title attribute is attached so hovering the line reveals its full content
  // even when it's truncated by ellipsis.
  secondaryField: string;
  // CSS class for the grid container. Defaults to 'list-grid-2'; pass e.g.
  // 'list-grid-3' to switch column count.
  gridClassName?: string;
  editInitialValues?: (item: T) => Record<string, unknown>;
}

/** Generic reusable list component for CRUD config management with cards. */
export default function ConfigList<T extends { id: number }>({
  api,
  formFields,
  i18nPrefix,
  onSelectConfig,
  showCreate,
  onCreateClose,
  onConfigChanged,
  beforeDelete,
  primaryField,
  secondaryField,
  gridClassName,
  editInitialValues,
}: ConfigListProps<T>) {
  const { t } = useTranslation();
  const [configs, setConfigs] = useState<T[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalVisible, setModalVisible] = useState(false);
  const [editModalVisible, setEditModalVisible] = useState(false);
  const [form] = Form.useForm();
  const [editForm] = Form.useForm();
  const [editingConfig, setEditingConfig] = useState<T | null>(null);

  const loadConfigs = async () => {
    setLoading(true);
    try {
      const response = await api.list();
      setConfigs(response.data);
    } catch (error) {
      logger.error(`Failed to load ${i18nPrefix}:`, error);
      message.error(t('messages.loadFailed'));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadConfigs();
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
      const response = await api.create(values);
      setConfigs([response.data, ...configs]);
      setModalVisible(false);
      form.resetFields();
      onCreateClose?.();
      message.success(t(`${i18nPrefix}.createSuccess`));
      onSelectConfig?.(response.data);
      onConfigChanged?.();
    } catch (error) {
      logger.error(`Failed to create ${i18nPrefix}:`, error);
      message.error(t(`${i18nPrefix}.createFailed`));
    }
  };

  const handleUpdate = async (values: Record<string, unknown>) => {
    if (!editingConfig) return;

    try {
      const response = await api.update(editingConfig.id, values);
      const index = configs.findIndex(c => c.id === editingConfig.id);
      if (index !== -1) {
        const newConfigs = [...configs];
        newConfigs[index] = response.data;
        setConfigs(newConfigs);
      }
      setEditModalVisible(false);
      editForm.resetFields();
      setEditingConfig(null);
      message.success(t(`${i18nPrefix}.updateSuccess`));
      onSelectConfig?.(response.data);
      onConfigChanged?.();
    } catch (error) {
      logger.error(`Failed to update ${i18nPrefix}:`, error);
      message.error(t(`${i18nPrefix}.updateFailed`));
    }
  };

  const handleDelete = async (configId: number, e: React.MouseEvent) => {
    e.stopPropagation();

    confirmDelete({
      title: t(`${i18nPrefix}.confirmDeleteTitle`),
      content: t(`${i18nPrefix}.confirmDelete`),
      okText: t('common.delete'),
      cancelText: t('common.cancel'),
      onOk: async () => {
        try {
          if (beforeDelete && !(await beforeDelete(configId))) return;
          await api.delete(configId);
          setConfigs(configs.filter(c => c.id !== configId));
          message.success(t(`${i18nPrefix}.deleteSuccess`));
          if (onSelectConfig && editingConfig?.id === configId) {
            onSelectConfig(null);
          }
          onConfigChanged?.();
        } catch (error) {
          logger.error(`Failed to delete ${i18nPrefix}:`, error);
          message.error(t(`${i18nPrefix}.deleteFailed`));
        }
      },
    });
  };

  const handleEdit = (config: T) => {
    setEditingConfig(config);
    editForm.setFieldsValue(
      editInitialValues
        ? editInitialValues(config)
        : formFields.reduce((acc, field) => {
            acc[field.name] = (config as Record<string, unknown>)[field.name] ?? '';
            return acc;
          }, {} as Record<string, unknown>)
    );
    setEditModalVisible(true);
  };

  const renderFormFields = () =>
    formFields.map(field => (
      <Form.Item
        key={field.name}
        label={t(`${i18nPrefix}.${field.labelKey}`)}
        name={field.name}
        rules={field.required ? [{ required: true, message: t(`${i18nPrefix}.${field.placeholderKey}`) }] : undefined}
      >
        {field.type === 'password' ? (
          <Input.Password placeholder={t(`${i18nPrefix}.${field.placeholderKey}`)} />
        ) : field.type === 'textarea' ? (
          <Input.TextArea placeholder={t(`${i18nPrefix}.${field.placeholderKey}`)} rows={field.rows || 2} />
        ) : (
          <Input placeholder={t(`${i18nPrefix}.${field.placeholderKey}`)} />
        )}
      </Form.Item>
    ));

  return (
    <>
      <div>
        {loading ? (
          <div className="empty-state-text">{t('sidebar.loading')}</div>
        ) : configs.length === 0 ? (
          <div className="empty-state-text">{t(`${i18nPrefix}.noConfig`)}</div>
        ) : (
          <div className={gridClassName || 'list-grid-2'}>
            {configs.map(config => {
              const record = config as Record<string, unknown>;
              const primary = String(record[primaryField]);
              const secondary = String(record[secondaryField]);
              return (
              <div key={config.id} className="item-card">
                <div className="item-card-header">
                  <div className="item-card-info">
                    <div className="item-card-name">
                      {primary}
                    </div>
                    <div className="item-card-desc" title={secondary}>
                      {secondary}
                    </div>
                  </div>
                </div>
                <CardActions
                  onEdit={(e) => { e.stopPropagation(); handleEdit(config); }}
                  onDelete={(e) => handleDelete(config.id, e)}
                />
              </div>
              );
            })}
          </div>
        )}
      </div>

      <Modal
        title={t(`${i18nPrefix}.create`)}
        open={modalVisible}
        onOk={() => form.submit()}
        onCancel={handleModalClose}
        okText={t('common.create')}
        cancelText={t('common.cancel')}
      >
        <Form
          form={form}
          layout="vertical"
          onFinish={handleCreate}
          style={{ marginTop: '16px' }}
        >
          {renderFormFields()}
        </Form>
      </Modal>

      <Modal
        title={t(`${i18nPrefix}.edit`)}
        open={editModalVisible}
        onOk={() => editForm.submit()}
        onCancel={() => {
          setEditModalVisible(false);
          editForm.resetFields();
          setEditingConfig(null);
        }}
        okText={t('common.update')}
        cancelText={t('common.cancel')}
      >
        <Form
          form={editForm}
          layout="vertical"
          onFinish={handleUpdate}
          style={{ marginTop: '16px' }}
        >
          {renderFormFields()}
        </Form>
      </Modal>
    </>
  );
}
