import { useCallback, useEffect, useMemo, useState } from 'react';
import { Form, Input, InputNumber, Modal, Select, Spin, Switch, message } from 'antd';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import type { TTSProviderDefinition, TTSRenderer } from '../types';
import { ttsRendererApi } from '../services/api';
import { logger } from '../logger';
import CardActions from './CardActions';

interface JSONSchema {
  type?: string;
  format?: string;
  enum?: unknown[];
  default?: unknown;
  minimum?: number;
  maximum?: number;
  required?: string[];
  properties?: Record<string, JSONSchema>;
  'x-ui'?: JSONSchemaPresentation;
}

interface JSONSchemaPresentation {
  label?: LocalizedText;
  description?: LocalizedText;
}

interface LocalizedText {
  [language: string]: string;
}

interface TTSRendererListProps {
  showCreate?: boolean;
  onCreateClose?: () => void;
}

// TTSRendererList creates renderer connections from the Adapter-provided
// schema. It deliberately does not hard-code any provider credential fields.
export default function TTSRendererList({ showCreate, onCreateClose }: TTSRendererListProps) {
  const { t, i18n } = useTranslation();
  const [providers, setProviders] = useState<TTSProviderDefinition[]>([]);
  const [renderers, setRenderers] = useState<TTSRenderer[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [editingRendererID, setEditingRendererID] = useState<number | null>(null);
  const [configuredSecretFields, setConfiguredSecretFields] = useState<string[]>([]);
  const [form] = Form.useForm();
  const selectedProviderID = Form.useWatch('provider', form);

  const selectedProvider = useMemo(
    () => providers.find(provider => provider.provider === selectedProviderID),
    [providers, selectedProviderID],
  );
  const selectedSchema = useMemo(() => parseSchema(selectedProvider), [selectedProvider]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [providerResponse, rendererResponse] = await Promise.all([
        ttsRendererApi.listProviders(),
        ttsRendererApi.list(),
      ]);
      setProviders(providerResponse.data);
      setRenderers(rendererResponse.data);
    } catch (error) {
      logger.error('Failed to load TTS renderer configuration', error);
      message.error(t('ttsConfig.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    if (showCreate) {
      setEditingRendererID(null);
      setConfiguredSecretFields([]);
      form.resetFields();
      setModalOpen(true);
    }
  }, [showCreate, form]);

  const closeModal = () => {
    setModalOpen(false);
    setEditingRendererID(null);
    setConfiguredSecretFields([]);
    form.resetFields();
    onCreateClose?.();
  };

  const handleProviderChange = () => {
    // Provider configuration is protocol-specific. Never carry a prior
    // provider's credentials or endpoint fields into a newly selected one.
    form.setFieldValue('connection_config', {});
  };

  const handleSave = async (values: Record<string, unknown>) => {
    setSaving(true);
    try {
      const payload = {
        name: String(values.name),
        connection_config: (values.connection_config as Record<string, unknown>) ?? {},
        description: String(values.description ?? ''),
      };
      if (editingRendererID === null) {
        const response = await ttsRendererApi.create({ ...payload, provider: Number(values.provider) });
        setRenderers(previous => [...previous, response.data]);
        message.success(t('ttsConfig.createSuccess'));
      } else {
        const response = await ttsRendererApi.update(editingRendererID, payload);
        setRenderers(previous => previous.map(item => item.id === editingRendererID ? response.data : item));
        message.success(t('ttsConfig.updateSuccess'));
      }
      closeModal();
    } catch (error) {
      logger.error('Failed to save TTS renderer', error, 'renderer_id', editingRendererID);
      message.error(t(editingRendererID === null ? 'ttsConfig.createFailed' : 'ttsConfig.updateFailed'));
    } finally {
      setSaving(false);
    }
  };

  const handleEdit = async (renderer: TTSRenderer) => {
    setSaving(true);
    try {
      const response = await ttsRendererApi.get(renderer.id);
      const detail = response.data;
      setEditingRendererID(detail.id);
      setConfiguredSecretFields(detail.configured_secret_fields);
      form.setFieldsValue({
        name: detail.name,
        provider: detail.provider,
        connection_config: detail.connection_config,
        description: detail.description,
      });
      setModalOpen(true);
    } catch (error) {
      logger.error('Failed to load TTS renderer detail', error, 'renderer_id', renderer.id);
      message.error(t('ttsConfig.loadFailed'));
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = (renderer: TTSRenderer) => {
    Modal.confirm({
      title: t('ttsConfig.confirmDeleteTitle'),
      content: t('ttsConfig.confirmDelete', { name: renderer.name }),
      okText: t('common.delete'),
      okButtonProps: { danger: true },
      cancelText: t('common.cancel'),
      onOk: async () => {
        try {
          await ttsRendererApi.delete(renderer.id);
          setRenderers(previous => previous.filter(item => item.id !== renderer.id));
          message.success(t('ttsConfig.deleteSuccess'));
        } catch (error) {
          logger.error('Failed to delete TTS renderer', error, 'renderer_id', renderer.id);
          message.error(t('ttsConfig.deleteFailed'));
          throw error;
        }
      },
    });
  };

  const providerName = (providerID: number) =>
    providers.find(provider => provider.provider === providerID)?.name ?? `${t('ttsConfig.unknownProvider')} (${providerID})`;

  return (
    <>
      <p className="panel-note">{t('ttsConfig.description')}</p>
      {loading ? (
        <div className="empty-state-text"><Spin size="small" /> {t('sidebar.loading')}</div>
      ) : renderers.length === 0 ? (
        <div className="empty-state-text">{t('ttsConfig.noConfig')}</div>
      ) : (
        <div className="list-grid-2">
          {renderers.map(renderer => (
            <div key={renderer.id} className="item-card">
              <div className="item-card-header">
                <div className="item-card-info">
                  <div className="item-card-name">{renderer.name}</div>
                  <div className="item-card-desc" title={renderer.description || providerName(renderer.provider)}>
                    {providerName(renderer.provider)}{renderer.description ? ` · ${renderer.description}` : ''}
                  </div>
                </div>
              </div>
              <CardActions
                onEdit={(event) => { event.stopPropagation(); void handleEdit(renderer); }}
                onDelete={(event) => { event.stopPropagation(); handleDelete(renderer); }}
              />
            </div>
          ))}
        </div>
      )}

      <Modal
        title={t(editingRendererID === null ? 'ttsConfig.create' : 'ttsConfig.edit')}
        open={modalOpen}
        onOk={() => form.submit()}
        onCancel={closeModal}
        okButtonProps={{ loading: saving }}
        okText={t(editingRendererID === null ? 'common.create' : 'common.save')}
        cancelText={t('common.cancel')}
        destroyOnHidden
      >
        <Form form={form} layout="vertical" onFinish={handleSave}>
          <Form.Item name="name" label={t('ttsConfig.name')} rules={[{ required: true, message: t('ttsConfig.nameRequired') }]}>
            <Input placeholder={t('ttsConfig.namePlaceholder')} />
          </Form.Item>
          <Form.Item name="provider" label={t('ttsConfig.provider')} rules={[{ required: true, message: t('ttsConfig.providerRequired') }]}>
            <Select
              placeholder={t('ttsConfig.providerPlaceholder')}
              options={providers.map(provider => ({ value: provider.provider, label: provider.name }))}
              onChange={handleProviderChange}
              disabled={editingRendererID !== null}
            />
          </Form.Item>
          {selectedProvider && <p className="tts-provider-description">{selectedProvider.description}</p>}
          {selectedProvider?.terms_url && (
            <p className="tts-terms-note">
              {t('ttsConfig.providerTermsNotice')}{' '}
              <a href={selectedProvider.terms_url} target="_blank" rel="noreferrer">{t('ttsConfig.readTerms')}</a>
            </p>
          )}
          {selectedSchema && renderSchemaFields(selectedSchema, [], i18n.language, t, new Set(configuredSecretFields), editingRendererID !== null)}
          <Form.Item name="description" label={t('ttsConfig.rendererDescription')}>
            <Input.TextArea rows={2} placeholder={t('ttsConfig.rendererDescriptionPlaceholder')} />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
}

function parseSchema(provider: TTSProviderDefinition | undefined): JSONSchema | null {
  if (!provider) return null;
  try {
    const schema = JSON.parse(provider.connection_config_json_schema) as JSONSchema;
    return schema.type === 'object' ? schema : null;
  } catch (error) {
    logger.error('TTS provider returned invalid configuration schema', error, 'provider', provider.provider);
    return null;
  }
}

function renderSchemaFields(
  schema: JSONSchema,
  path: string[],
  language: string,
  t: TFunction,
  configuredSecretFields: Set<string>,
  editing: boolean,
) {
  return Object.entries(schema.properties ?? {}).map(([key, field]) => {
    const fieldPath = [...path, key];
    const required = schema.required?.includes(key) ?? false;
    const label = localizedText(field['x-ui']?.label, language);
    const description = localizedText(field['x-ui']?.description, language);
    if (!label) {
      // The backend rejects these schemas at Adapter registration. Keep the
      // browser defensive so raw protocol keys can never become UI labels.
      logger.error('TTS provider field is missing a localized label', { field_path: fieldPath.join('.') });
      return null;
    }
    if (field.type === 'object') {
      return (
        <div className="tts-schema-group" key={fieldPath.join('.')}>
          <div className="tts-schema-group-title">{label}{description ? ` — ${description}` : ''}</div>
          {renderSchemaFields(field, fieldPath, language, t, configuredSecretFields, editing)}
        </div>
      );
    }
    const name = ['connection_config', ...fieldPath];
    const isConfiguredSecret = field.format === 'password' && configuredSecretFields.has(fieldPath.join('.'));
    const rules = required && !isConfiguredSecret ? [{ required: true, message: t('ttsConfig.fieldRequired', { field: label }) }] : undefined;
    const fieldLabel = description ? `${label} — ${description}` : label;
    if (field.type === 'boolean') {
      return (
        <Form.Item key={fieldPath.join('.')} name={name} label={fieldLabel} valuePropName="checked" initialValue={field.default ?? false}>
          <Switch />
        </Form.Item>
      );
    }
    if (field.type === 'integer' || field.type === 'number') {
      return (
        <Form.Item key={fieldPath.join('.')} name={name} label={fieldLabel} rules={rules} initialValue={field.default}>
          <InputNumber
            style={{ width: '100%' }}
            min={field.minimum}
            max={field.maximum}
            precision={field.type === 'integer' ? 0 : undefined}
          />
        </Form.Item>
      );
    }
    if (field.enum) {
      return (
        <Form.Item key={fieldPath.join('.')} name={name} label={fieldLabel} rules={rules} initialValue={field.default}>
          <Select options={field.enum.map(value => ({ value, label: String(value) }))} />
        </Form.Item>
      );
    }
    return (
      <Form.Item key={fieldPath.join('.')} name={name} label={fieldLabel} rules={rules} initialValue={field.default}>
        {field.format === 'password'
          ? <Input.Password placeholder={editing && isConfiguredSecret ? t('ttsConfig.secretConfigured') : undefined} />
          : <Input />}
      </Form.Item>
    );
  });
}

// localizedText selects an exact locale, then any matching language-family
// variant (for example zh -> zh-CN), then English. It intentionally has no
// protocol-key fallback.
function localizedText(values: LocalizedText | undefined, language: string): string {
  if (!values) return '';
  const normalizedLanguage = language.toLowerCase();
  const baseLanguage = normalizedLanguage.split('-')[0];
  const exact = Object.entries(values).find(([locale]) => locale.toLowerCase() === normalizedLanguage)?.[1];
  const base = Object.entries(values).find(([locale]) => locale.toLowerCase() === baseLanguage)?.[1];
  const family = Object.entries(values).find(([locale]) => locale.toLowerCase().startsWith(`${baseLanguage}-`))?.[1];
  return exact ?? base ?? family ?? values.en ?? '';
}
