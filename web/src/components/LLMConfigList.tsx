import { useEffect, useState } from 'react';
import { QuestionCircleOutlined, WarningOutlined } from '@ant-design/icons';
import { Tag, Tooltip, message } from 'antd';
import { useTranslation } from 'react-i18next';
import ConfigList from './ConfigList';
import { llmConfigApi, systemLLMConfigApi } from '../services/api';
import { logger } from '../logger';
import { LLMProbeStatus, type LLMConfig } from '../types';

const probeStatusKeys = {
  [LLMProbeStatus.Unverified]: 'probe_unverified',
  [LLMProbeStatus.Passed]: 'probe_passed',
  [LLMProbeStatus.Failed]: 'probe_failed',
} as const;

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
  const [refreshKey, setRefreshKey] = useState(0);
  const [probingID, setProbingID] = useState<number | null>(null);

  const probeDetails = (config: LLMConfig) => config.probe.status === LLMProbeStatus.Unverified
    ? t('llmConfig.notTested')
    : [
      `${t('llmConfig.requiredTools')}: ${config.probe.required_tools.ok ? t('llmConfig.probe_passed') : config.probe.required_tools.error || t('llmConfig.probe_failed')}`,
      `${t('llmConfig.structuredOutput')}: ${config.probe.structured_output.ok ? t('llmConfig.probe_passed') : config.probe.structured_output.error || t('llmConfig.probe_failed')}`,
      config.probe.checked_at && `${t('llmConfig.checkedAt')}: ${new Date(config.probe.checked_at).toLocaleString()}`,
    ].filter(Boolean).join('\n');

  const showProbeWarning = (config: LLMConfig) => {
    if (config.probe.status === LLMProbeStatus.Failed) {
      message.warning(`${t('llmConfig.probeFailed')} ${probeDetails(config)}`, 8);
    }
  };

  const handleRetest = async (id: number) => {
    setProbingID(id);
    try {
      const { data } = await llmConfigApi.probe(id);
      setRefreshKey(value => value + 1);
      onConfigChanged?.();
      if (data.probe.status === LLMProbeStatus.Passed) message.success(t('llmConfig.probePassed'));
      else showProbeWarning(data);
    } catch (error) {
      logger.error('Failed to retest LLM config', error);
      message.error(t('llmConfig.probeRequestFailed'));
    } finally {
      setProbingID(null);
    }
  };

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
      onSaved={showProbeWarning}
      refreshKey={refreshKey}
      onCreated={(_created, configs) => {
        // Soft constraint: only the first config can be auto-promoted to the
        // system LLM by the backend, so re-query the system config only in
        // that case to avoid useless requests on every create.
        if (configs.length === 1) fetchSystemLLMId();
      }}
      beforeDelete={beforeDelete}
      renderMeta={(config) => <>
        {config.id === systemLLMId && <Tooltip title={t('llmConfig.systemTag')} trigger={['hover', 'focus']}>
          <Tag color="gold" role="img" aria-label={t('llmConfig.systemTag')} tabIndex={0} style={{ fontSize: 11 }}>S</Tag>
        </Tooltip>}
        {config.probe.status !== LLMProbeStatus.Passed && <Tooltip title={probeDetails(config)} trigger={['hover', 'focus']}>
          <Tag role="img" aria-label={t(`llmConfig.${probeStatusKeys[config.probe.status]}`)} tabIndex={0}
            color={config.probe.status === LLMProbeStatus.Failed ? 'red' : 'default'}>
            {config.probe.status === LLMProbeStatus.Failed
              ? <WarningOutlined aria-hidden="true" />
              : <QuestionCircleOutlined aria-hidden="true" />}
          </Tag>
        </Tooltip>}
      </>}
      renderExtraAction={(config) => <>
        {config.id !== systemLLMId && <Tooltip title={config.probe.status === LLMProbeStatus.Failed ? t('llmConfig.setSystemUnavailable') : t('llmConfig.setSystemTitle')} trigger={['hover', 'focus']}>
          <span tabIndex={config.probe.status === LLMProbeStatus.Failed ? 0 : undefined}
            aria-label={config.probe.status === LLMProbeStatus.Failed ? t('llmConfig.setSystemUnavailable') : undefined}>
            <button className="item-card-action-btn" type="button"
              aria-label={t(config.probe.status === LLMProbeStatus.Failed ? 'llmConfig.setSystemUnavailable' : 'llmConfig.setSystemTitle')}
              disabled={config.probe.status === LLMProbeStatus.Failed} style={{ fontSize: 13, fontWeight: 700 }}
              onClick={(e) => { e.stopPropagation(); handleSetSystem(config.id); }}>S</button>
          </span>
        </Tooltip>}
        <Tooltip title={t('llmConfig.retest')}>
          <button className="item-card-action-btn" type="button" aria-label={t('llmConfig.retest')} disabled={probingID === config.id}
            onClick={(e) => { e.stopPropagation(); void handleRetest(config.id); }}>↻</button>
        </Tooltip>
      </>}
    />
  );
}
