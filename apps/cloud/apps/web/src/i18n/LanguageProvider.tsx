// The language of the cloud app (ADR-0009 rule 7, issue #436): English or Spanish, chosen by the person with the switch in
// the top bar, remembered in this browser, and otherwise taken from the browser's own language. The words of a page are
// written next to each other, `t('Create node', 'Crear nodo')`, so a string without its translation cannot be written.
import { LangProvider as UiLangProvider, type Lang } from '@localai/ui';
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';

export type { Lang };

const STORAGE_KEY = 'localai.lang';

interface LanguageValue {
  lang: Lang;
  setLang: (lang: Lang) => void;
}

const LanguageContext = createContext<LanguageValue>({ lang: 'en', setLang: () => undefined });

/** The first of the saved choice and the browser's language; `es`, `es-MX` ... give Spanish, anything else English. */
export function initialLang(): Lang {
  try {
    const saved = window.localStorage.getItem(STORAGE_KEY);
    if (saved === 'en' || saved === 'es') return saved;
  } catch {
    // Private windows and blocked storage: the page works without a remembered choice.
  }
  const browser = typeof navigator !== 'undefined' ? (navigator.language ?? '').toLowerCase() : '';
  return browser === 'es' || browser.startsWith('es-') ? 'es' : 'en';
}

export function LanguageProvider({ children, initial }: { children: ReactNode; initial?: Lang }) {
  const [lang, setLangState] = useState<Lang>(() => initial ?? initialLang());
  useEffect(() => {
    document.documentElement.lang = lang;
  }, [lang]);
  const setLang = useCallback((next: Lang) => {
    setLangState(next);
    try {
      window.localStorage.setItem(STORAGE_KEY, next);
    } catch {
      // Not remembered; the choice still applies until the page is closed.
    }
  }, []);
  const value = useMemo(() => ({ lang, setLang }), [lang, setLang]);
  return (
    <LanguageContext.Provider value={value}>
      <UiLangProvider lang={lang}>{children}</UiLangProvider>
    </LanguageContext.Provider>
  );
}

export function useLanguage(): LanguageValue {
  return useContext(LanguageContext);
}

/** `t(english, spanish)`: the text in the language in force. */
export function useT(): (en: string, es: string) => string {
  const { lang } = useLanguage();
  return useCallback((en: string, es: string) => (lang === 'es' ? es : en), [lang]);
}
