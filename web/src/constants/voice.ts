/**
 * Canonical language-region choices for an AgentVoice identity.
 *
 * These are product metadata rather than renderer protocol parameters. Keeping
 * the catalogue here prevents provider-specific API field names from leaking
 * into the Agent configuration UI.
 */
export interface VoiceLocaleOption {
  value: string;
  label: {
    zh: string;
    en: string;
  };
}

export const VOICE_LOCALE_OPTIONS: readonly VoiceLocaleOption[] = [
  { value: 'zh-CN', label: { zh: '中文（简体，中国）', en: 'Chinese (Simplified, China)' } },
  { value: 'en-US', label: { zh: '英语（美国）', en: 'English (United States)' } },
  { value: 'ja-JP', label: { zh: '日语（日本）', en: 'Japanese (Japan)' } },
];
