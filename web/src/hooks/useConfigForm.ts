import { useState, useCallback, useEffect, useRef } from 'react';
import { message } from 'antd';
import { useTranslation } from 'react-i18next';
import { logger } from '../logger';

interface UseConfigFormOptions<T> {
  /** API call to load current config. Return null means "ignore" (don't set form). */
  loadApi: () => Promise<{ data: T } | null>;
  /** API call to save. Receives flattened form values, returns updated config. */
  saveApi: (values: Partial<T>) => Promise<{ data: T }>;
  /** i18n prefix for load/save messages, e.g. "searchConfig" → t("searchConfig.loadError") */
  i18nPrefix: string;
  /** Called after successful save with the returned config */
  onSaved?: (config: T) => void;
  /** When this changes, re-load (like a refresh key). Undefined = no external refresh. */
  refreshKey?: number;
  /** If true, skip the initial load entirely (e.g. welcome/onboarding mode). */
  skipInitialLoad?: boolean;
  /** Optional validation before save. Return a string error message to block save. */
  beforeSave?: (values: Partial<T>) => string | null;
  /** Called after data is loaded, to populate form fields etc. */
  onLoaded?: (data: T) => void;
}

interface UseConfigFormReturn<T> {
  loading: boolean;
  saving: boolean;
  dirty: boolean;
  config: T | null;
  loadData: () => Promise<T | null>;
  handleSave: (values: Partial<T>) => Promise<boolean>;
  /** Call in onValuesChange to mark form as dirty */
  markDirty: () => void;
}

/**
 * Shared config form logic: load → form fill → track dirty → save → success.
 * Handles loading/saving/error states and i18n messages automatically.
 */
export function useConfigForm<T>(opts: UseConfigFormOptions<T>): UseConfigFormReturn<T> {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [config, setConfig] = useState<T | null>(null);
  const optsRef = useRef(opts);
  useEffect(() => {
    optsRef.current = opts;
  }, [opts]);

  const markDirty = useCallback(() => setDirty(true), []);

  const loadData = useCallback(async (): Promise<T | null> => {
    setLoading(true);
    try {
      const res = await optsRef.current.loadApi();
      if (!res) return null; // skip (e.g. welcome mode)
      setConfig(res.data);
      setDirty(false);
      optsRef.current.onLoaded?.(res.data);
      return res.data;
    } catch (err) {
      logger.error(`[${optsRef.current.i18nPrefix}] load failed`, err);
      message.error(t(`${optsRef.current.i18nPrefix}.loadError`));
      return null;
    } finally {
      setLoading(false);
    }
  }, [t]);

  const handleSave = useCallback(async (values: Partial<T>): Promise<boolean> => {
    const { beforeSave, saveApi, i18nPrefix, onSaved } = optsRef.current;

    if (beforeSave) {
      const error = beforeSave(values);
      if (error) {
        message.error(error);
        return false;
      }
    }

    setSaving(true);
    try {
      const res = await saveApi(values);
      setConfig(res.data);
      setDirty(false);
      message.success(t(`${i18nPrefix}.saveSuccess`));
      onSaved?.(res.data);
      return true;
    } catch (err) {
      logger.error(`[${i18nPrefix}] save failed`, err);
      message.error(t(`${i18nPrefix}.saveError`));
      return false;
    } finally {
      setSaving(false);
    }
  }, [t]);

  // Load on mount (unless skipInitialLoad) and on refreshKey change
  useEffect(() => {
    if (opts.skipInitialLoad) return;
    loadData();
  }, [opts.refreshKey, opts.skipInitialLoad, loadData]);

  return { loading, saving, dirty, config, loadData, handleSave, markDirty };
}
