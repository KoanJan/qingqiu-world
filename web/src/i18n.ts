import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import { storage } from './services/storage';
import zh from './locales/zh.json';
import en from './locales/en.json';

const LANGUAGE_KEY = 'qingqiu-world-language';

const getSavedLanguage = (): string => {
  const saved = storage.getRaw(LANGUAGE_KEY);
  if (saved && (saved === 'zh' || saved === 'en')) {
    return saved;
  }
  return 'en';
};

i18n
  .use(initReactI18next)
  .init({
    resources: {
      zh: { translation: zh },
      en: { translation: en }
    },
    lng: getSavedLanguage(),
    fallbackLng: 'en',
    interpolation: {
      escapeValue: false
    }
  });

/** Switches the application language. Persists to both localStorage (for
 *  i18next initialisation) and config.json (via useAppearance). */
export const changeLanguage = (lang: string) => {
  storage.setRaw(LANGUAGE_KEY, lang);
  i18n.changeLanguage(lang);
};

/** Returns the currently active language code. */
export const getCurrentLanguage = (): string => {
  return i18n.language;
};

export default i18n;
