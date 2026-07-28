import { useState, useCallback, useRef, useEffect } from 'react';
import { getDynamicServerBaseUrl, initApiClient } from '../services/api';
import { logger } from '../logger';

// ---- types ----

export type BgMode = 'color' | 'image';

export interface AppearanceSettings {
  /** UI language code ('zh' | 'en'). */
  language: string;
  /** Background mode: solid colour or image. */
  bgMode: BgMode;
  /** Hex colour used when bgMode === 'color'. */
  bgColor: string;
  /** Background image identifier — a disk filename, preset filename, or
   *  legacy base64 data URL. */
  bgImage: string;
  /** Source type of bgImage — "preset" or "upload". */
  bgImageSource: string;
  /** User-added custom hex colours. */
  customColors: string[];
  /** Glass overlay opacity, 0-1. */
  glassOpacity: number;
  /** Glass blur radius in px. */
  glassBlur: number;
}

// ---- system presets ----

/** System preset background images — built-in assets (src/assets/backgrounds/).
 *  Dynamically discovered at build time via Vite's import.meta.glob;
 *  each entry holds the {filename, url} pair for direct use in galleries. */
const presetModules = import.meta.glob('/src/assets/backgrounds/*.{png,jpg,jpeg,webp,gif,bmp}', { eager: true, query: '?url', import: 'default' });
/** Pre-loaded background image entries sorted by filename. */
export const PRESET_BACKGROUNDS: { filename: string; url: string }[] = Object.entries(presetModules)
  .map(([path, url]) => ({ filename: (path as string).split('/').pop()!, url: url as string }))
  .sort((a, b) => a.filename.localeCompare(b.filename));

/** 6 default solid colours shown as quick-select swatches. */
export const COLOR_PRESETS = [
  '#f3f4f6', // light grey
  '#f5f0eb', // warm cream
  '#e8f4f8', // light blue
  '#f0ebe4', // sand
  '#e8f5e9', // light green
  '#f3e5f5', // light lavender
];

// ---- defaults ----

const DEFAULTS: AppearanceSettings = {
  language: 'zh',
  bgMode: 'color',
  bgColor: '#f3f4f6',
  bgImage: '',
  bgImageSource: '',
  customColors: [],
  glassOpacity: 0.65,
  glassBlur: 10,
};

/** Background-related defaults used by resetBackground. */
const BG_DEFAULTS = {
  bgMode: DEFAULTS.bgMode,
  bgColor: DEFAULTS.bgColor,
  bgImage: DEFAULTS.bgImage,
  bgImageSource: DEFAULTS.bgImageSource,
  customColors: DEFAULTS.customColors,
} as const;

/** Overlay-related defaults used by resetOverlay. */
const OVERLAY_DEFAULTS = {
  glassOpacity: DEFAULTS.glassOpacity,
  glassBlur: DEFAULTS.glassBlur,
} as const;

// ---- API helpers ----

/** Resolve the server base URL for API calls, falling back to relative path.
 *  Synchronous — used in render (e.g. getBackgroundUrl).  May return a
 *  fallback until port resolution completes. */
export function apiBase(): string {
  const base = getDynamicServerBaseUrl();
  return base || '';
}

/** Resolve the server base URL after waiting for port resolution (IPC).
 *  Use for data-fetching functions that must hit the correct port. */
async function resolvedApiBase(): Promise<string> {
  await initApiClient();
  const base = getDynamicServerBaseUrl();
  return base || '';
}

/** Parse and validate API response JSON into AppearanceSettings,
 *  applying DEFAULTS for missing or invalid fields. */
function parseSettingsResponse(data: any): AppearanceSettings {
  return {
    language: (data.language === 'zh' || data.language === 'en') ? data.language : DEFAULTS.language,
    bgMode: (data.bgMode === 'color' || data.bgMode === 'image') ? data.bgMode : DEFAULTS.bgMode,
    bgColor: data.bgColor || DEFAULTS.bgColor,
    bgImage: data.bgImage || '',
    bgImageSource: (data.bgImageSource === 'preset' || data.bgImageSource === 'upload') ? data.bgImageSource : '',
    customColors: Array.isArray(data.customColors) ? data.customColors : [],
    glassOpacity: typeof data.glassOpacity === 'number' ? data.glassOpacity : DEFAULTS.glassOpacity,
    glassBlur: typeof data.glassBlur === 'number' ? data.glassBlur : DEFAULTS.glassBlur,
  };
}

/** Fetch current appearance settings from the Go backend. */
async function fetchSettings(): Promise<AppearanceSettings> {
  const base = await resolvedApiBase();
  const resp = await fetch(`${base}/api/appearance/settings`);
  if (!resp.ok) throw new Error(`GET settings failed: ${resp.status}`);
  const result = await resp.json();
  return parseSettingsResponse(result?.data ?? result);
}

/** Send a partial settings update to the Go backend. */
async function putSettings(partial: Partial<AppearanceSettings>): Promise<AppearanceSettings> {
  const base = await resolvedApiBase();
  const resp = await fetch(`${base}/api/appearance/settings`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(partial),
  });
  if (!resp.ok) throw new Error(`PATCH settings failed: ${resp.status}`);
  const result = await resp.json();
  return parseSettingsResponse(result?.data ?? result);
}

/** Build the full display URL for a background image identifier. */
export function getBackgroundUrl(
  bgImage: string | null | undefined,
  bgImageSource?: string,
): string | undefined {
  if (!bgImage) return undefined;
  // Legacy base64 — use directly
  if (bgImage.startsWith('data:')) return bgImage;
  // System preset — look up from build-time discovered assets
  if (bgImageSource === 'preset') {
    const entry = PRESET_BACKGROUNDS.find((p) => p.filename === bgImage);
    return entry?.url;
  }
  // User-uploaded file — served via API
  const base = apiBase();
  return `${base}/api/appearance/backgrounds/${bgImage}`;
}

// ---- migration from localStorage ----

const OLD_KEYS = [
  'appearance-bg-mode',
  'appearance-bg-color',
  'appearance-bg-image',
  'appearance-custom-colors',
  'appearance-glass-opacity',
  'appearance-glass-blur',
  'qingqiu-world-language',
];

/** Attempt to migrate old localStorage settings to the file-backed API. */
async function migrateFromLocalStorage(): Promise<AppearanceSettings | null> {
  try {
    const rawBgMode = localStorage.getItem('appearance-bg-mode');
    if (!rawBgMode && !localStorage.getItem('appearance-bg-image')) {
      return null; // No old data to migrate
    }

    const oldBgImage = localStorage.getItem('appearance-bg-image') || '';
    // Normalize legacy formats
    let normalizedBgImage = oldBgImage;
    if (oldBgImage.startsWith('appdata://')) {
      normalizedBgImage = oldBgImage.split('/').pop() || '';
    }

    let customColors: string[] = [];
    try {
      const raw = localStorage.getItem('appearance-custom-colors');
      if (raw) customColors = JSON.parse(raw);
    } catch { /* ignore */ }

    const migration: Partial<AppearanceSettings> = {
      language: localStorage.getItem('qingqiu-world-language') || DEFAULTS.language,
      bgMode: (rawBgMode === 'color' || rawBgMode === 'image') ? rawBgMode : DEFAULTS.bgMode,
      bgColor: localStorage.getItem('appearance-bg-color') || DEFAULTS.bgColor,
      bgImage: normalizedBgImage,
      customColors,
      glassOpacity: parseFloat(localStorage.getItem('appearance-glass-opacity') || '') || DEFAULTS.glassOpacity,
      glassBlur: parseInt(localStorage.getItem('appearance-glass-blur') || '', 10) || DEFAULTS.glassBlur,
    };

    const saved = await putSettings(migration);

    // Clean up old localStorage keys
    for (const key of OLD_KEYS) {
      localStorage.removeItem(key);
    }

    logger.info('[appearance] Migrated settings from localStorage to config.json');
    return saved;
  } catch (err) {
    logger.warn('[appearance] Failed to migrate from localStorage:', err);
    return null;
  }
}

// ---- hook ----

/**
 * Hook for user-customizable appearance settings.
 *
 * All settings are stored in data/settings/config.json via the Go backend API.
 * On first load, legacy localStorage data is migrated automatically.
 *
 * Supports two background modes:
 *   - colour: solid hex colour with 5 presets + custom colour picker
 *   - image:  system presets and user-uploaded images stored on disk
 *
 * Opacity / blur are independent sliders available in both modes.
 */
export default function useAppearance() {
  const [settings, setSettings] = useState<AppearanceSettings>(DEFAULTS);
  const [loading, setLoading] = useState(true);
  const initialized = useRef(false);

  // Ref for image dependencies
  const bgImageRef = useRef(settings.bgImage);
  useEffect(() => { bgImageRef.current = settings.bgImage; }, [settings.bgImage]);

  // Initialize from API on mount. Retries on failure until backend is ready,
  // because in packaged apps the frontend may start before the Go server.
  useEffect(() => {
    if (initialized.current) return;
    let cancelled = false;

    const load = async () => {
      if (cancelled) return;
      try {
        let s = await fetchSettings();

        // If settings are defaults, try to migrate from localStorage
        const isDefault =
          s.bgMode === DEFAULTS.bgMode &&
          s.bgColor === DEFAULTS.bgColor &&
          s.bgImage === DEFAULTS.bgImage &&
          s.customColors.length === 0 &&
          s.glassOpacity === DEFAULTS.glassOpacity &&
          s.glassBlur === DEFAULTS.glassBlur;

        if (isDefault) {
          const migrated = await migrateFromLocalStorage();
          if (migrated) s = migrated;
        }

        if (cancelled) return;

        // Sync i18n BEFORE updating React state — ensures the first render
        // after loading completes has the correct language.
        const { getCurrentLanguage, changeLanguage } = await import('../i18n');
        if (s.language !== getCurrentLanguage()) {
          changeLanguage(s.language);
        }

        initialized.current = true;
        setSettings(s);
      } catch (err) {
        if (cancelled) return;
        logger.warn('[appearance] Failed to load settings, retrying in 1s:', err);
        setTimeout(load, 1000);
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    load();

    return () => { cancelled = true; };
  }, []);

  // ---- mode ----

  // ---- language ----

  /** Update UI language — persists to config.json and calls i18n.changeLanguage. */
  const updateLanguage = useCallback(async (lang: string) => {
    // Dynamically import to avoid circular dependency with i18n
    const { changeLanguage } = await import('../i18n');
    changeLanguage(lang);
    // Optimistic update — UI reflects new language immediately
    setSettings(prev => ({ ...prev, language: lang }));
    try {
      const s = await putSettings({ language: lang });
      setSettings(s);
    } catch {
      logger.error('Failed to persist language setting');
    }
  }, []);

  // ---- colour ----

  /** Select a colour: atomically sets bgMode to 'color' and bgColor to the given value. */
  const selectColor = useCallback(async (color: string) => {
    const s = await putSettings({ bgMode: 'color', bgColor: color });
    setSettings(s);
  }, []);

  const addCustomColor = useCallback(async (color: string) => {
    setSettings((prev) => {
      if (prev.customColors.includes(color)) return prev;
      const next = { ...prev, customColors: [...prev.customColors, color] };
      // Fire-and-forget save (optimistic UI)
      putSettings({ customColors: next.customColors }).catch((err) =>
        logger.warn('[appearance] Failed to save custom colors:', err),
      );
      return next;
    });
  }, []);

  const removeCustomColor = useCallback(async (color: string) => {
    setSettings((prev) => {
      const next = { ...prev, customColors: prev.customColors.filter((c) => c !== color) };
      putSettings({ customColors: next.customColors }).catch((err) =>
        logger.warn('[appearance] Failed to save custom colors:', err),
      );
      return next;
    });
  }, []);

  // ---- image ----

  /** Select a background image identifier, atomically setting mode to 'image'.
   *  The caller passes both the filename and its source type ("preset" | "upload"). */
  const selectBgImage = useCallback(async (value: string, source: 'preset' | 'upload') => {
    const s = await putSettings({ bgMode: 'image', bgImage: value, bgImageSource: source });
    setSettings(s);
  }, []);

  /** Upload a user image and set it as the current background. */
  const uploadBgImage = useCallback(async (dataUrl: string) => {
    try {
      const resp = await fetch(dataUrl);
      if (!resp.ok) throw new Error(`Failed to decode data URL: ${resp.status}`);
      const blob = await resp.blob();
      const ext = blob.type.split('/')[1] || 'png';

      const formData = new FormData();
      formData.append('file', blob, `background.${ext}`);

      const uploadResp = await fetch(`${apiBase()}/api/appearance/backgrounds`, {
        method: 'POST',
        body: formData,
      });
      if (!uploadResp.ok) throw new Error(`Upload failed: ${uploadResp.status}`);

      const result = await uploadResp.json();
      const filename: string = result?.data?.filename;
      if (!filename) throw new Error('Upload succeeded but no filename returned');

      const s = await putSettings({ bgImage: filename, bgImageSource: 'upload' });
      setSettings(s);
    } catch (err) {
      logger.error('[appearance] Failed to upload background image:', err);
    }
  }, []);

  /** Delete a user-uploaded image and clear selection if it was the current one. */
  const deleteUserImage = useCallback(async (filename: string) => {
    try {
      await fetch(`${apiBase()}/api/appearance/backgrounds/${encodeURIComponent(filename)}`, {
        method: 'DELETE',
      });
    } catch (err) {
      logger.warn('[appearance] Failed to delete background file:', err);
    }

    if (bgImageRef.current === filename) {
      const s = await putSettings({ bgImage: '' });
      setSettings(s);
    }
  }, []);

  /** Fetch the list of user-uploaded image filenames from the backend. */
  const fetchUserImages = useCallback(async (): Promise<string[]> => {
    try {
      const resp = await fetch(`${apiBase()}/api/appearance/backgrounds`);
      if (!resp.ok) return [];
      const result = await resp.json();
      const files: { filename: string }[] = result?.data ?? [];
      return files.map((f) => f.filename);
    } catch (err) {
      logger.warn('[appearance] Failed to fetch user images:', err);
      return [];
    }
  }, []);

  // ---- glass ----

  /** Optimistically update and persist a glass (opacity/blur) setting. */
  const saveGlassSetting = useCallback(async (patch: Partial<Pick<AppearanceSettings, 'glassOpacity' | 'glassBlur'>>) => {
    setSettings((prev) => ({ ...prev, ...patch }));
    try {
      await putSettings(patch);
    } catch (err) {
      logger.warn('[appearance] Failed to save glass setting:', err);
    }
  }, []);

  const updateGlassOpacity = useCallback(async (value: number) => {
    await saveGlassSetting({ glassOpacity: Math.min(1, Math.max(0, value)) });
  }, [saveGlassSetting]);

  const updateGlassBlur = useCallback(async (value: number) => {
    await saveGlassSetting({ glassBlur: Math.max(0, value) });
  }, [saveGlassSetting]);

  // ---- reset ----

  const resetBackground = useCallback(async () => {
    const s = await putSettings(BG_DEFAULTS);
    setSettings(s);
  }, []);

  const resetOverlay = useCallback(async () => {
    const s = await putSettings(OVERLAY_DEFAULTS);
    setSettings(s);
  }, []);

  return {
    settings,
    loading,
    selectColor,
    addCustomColor,
    removeCustomColor,
    selectBgImage,
    uploadBgImage,
    deleteUserImage,
    fetchUserImages,
    updateGlassOpacity,
    updateGlassBlur,
    updateLanguage,
    resetBackground,
    resetOverlay,
  };
}
