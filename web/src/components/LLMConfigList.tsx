import { useEffect, useState } from 'react';
import { Tag, Tooltip, message } from 'antd';
import { useTranslation } from 'react-i18next';
import ConfigList from './ConfigList';
import { llmConfigApi, systemLLMConfigApi } from '../services/api';
import { logger } from '../logger';
import type { LLMConfig } from '../types';

const FORM_FIELDS = [
  { name: 'name', labelKey: 'name', placeholderKey: 'namePlaceholder', required: true },
  { name: 'model_id', labelKey: 'modelId', placeholderKey: 'modelIdPlaceholder', required: true },
  { name: 'base_url', labelKey: 'baseUrl', placeholderKey: 'baseUrlPlaceholder', required: true },
  { name: 'api_key', labelKey: 'apiKey', placeholderKey: 'apiKeyPlaceholder', required: true, type: 'password' as const },
  { name: 'description', labelKey: 'description', placeholderKey: 'descriptionPlaceholder', type: 'textarea' as const, rows: 2 },
];

interface LLMConfigListProps {
  onSelectConfig?: (config: LLMConfig | null) => void;
  showCreate?: boolean;
  onCreateClose?: () => void;
  onConfigChanged?: () => void;
  beforeDelete?: (id: number) => Promise<boolean>;
}

/**
 * LLM configuration list: wraps ConfigList with LLM-specific fields and the
 * system-LLM workflow. The card of the LLM currently acting as the system LLM
 * carries a top-right tag and hides the "set as system" action; every other
 * card shows a bottom-right "S" action that makes it the system LLM.
 */
export default function LLMConfigList({ onSelectConfig, showCreate, onCreateClose, onConfigChanged, beforeDelete }: LLMConfigListProps) {
  const { t } = useTranslation();
  // Singleton backend config: the llm_config_id currently acting as system LLM.
  const [systemLLMId, setSystemLLMId] = useState<number | null>(null);

  // Load which config currently acts as the system LLM. Reused on mount and
  // after creating the first LLM config (the backend auto-promotes it, so the
  // local tag state needs one re-sync).
  const fetchSystemLLMId = () => {
    systemLLMConfigApi.get()
      .then((res) => setSystemLLMId(res.data?.llm_config_id ?? null))
      .catch((error) => {
        logger.error('Failed to load system LLM config:', error);
        message.error(t('systemLLMConfig.loadError'));
      });
  };

  useEffect(() => {
    fetchSystemLLMId();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleSetSystem = async (id: number) => {
    try {
      await systemLLMConfigApi.update({ llm_config_id: id });
      setSystemLLMId(id);
      message.success(t('llmConfig.setSystemSuccess'));
      onConfigChanged?.();
    } catch (error) {
      logger.error('Failed to set system LLM config:', error);
      message.error(t('systemLLMConfig.saveError'));
    }
  };

  return (
    <ConfigList<LLMConfig>
      api={llmConfigApi}
      formFields={FORM_FIELDS}
      i18nPrefix="llmConfig"
      primaryField="model_id"
      secondaryField="name"
      gridClassName="list-grid-3"
      onSelectConfig={onSelectConfig}
      showCreate={showCreate}
      onCreateClose={onCreateClose}
      onConfigChanged={onConfigChanged}
      onCreated={(_created, configs) => {
        // Soft constraint: only the first config can be auto-promoted to the
        // system LLM by the backend, so re-query the system config only in
        // that case to avoid useless requests on every create.
        if (configs.length === 1) fetchSystemLLMId();
      }}
      beforeDelete={beforeDelete}
      renderMeta={(config) => config.id === systemLLMId ? (
        <Tag color="gold" style={{ fontSize: 11 }}>{t('llmConfig.systemTag')}</Tag>
      ) : null}
      renderExtraAction={(config) => config.id === systemLLMId ? null : (
        // antd Tooltip instead of the native title attribute — browsers delay
        // native tooltips by ~1s and that delay is not configurable.
        <Tooltip title={t('llmConfig.setSystemTitle')}>
          <button
            className="item-card-action-btn"
            style={{ fontSize: 13, fontWeight: 700 }}
            onClick={(e) => { e.stopPropagation(); handleSetSystem(config.id); }}
          >
            S
          </button>
        </Tooltip>
      )}
    />
  );
}
