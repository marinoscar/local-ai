// The language of the shared components (ADR-0009 rule 7: English and Spanish).
//
// The components take their words from the language the host provides. Without a provider the language is English, so
// a host that has not been made bilingual yet (and every existing test) renders exactly what it did. The host owns the
// choice and where it is remembered; this file only carries it down.
import { createContext, useContext, type ReactNode } from 'react';

export type Lang = 'en' | 'es';

const LangContext = createContext<Lang>('en');

/** Gives the shared components the language to speak. */
export function LangProvider({ lang, children }: { lang: Lang; children: ReactNode }) {
  return <LangContext.Provider value={lang}>{children}</LangContext.Provider>;
}

/** The language in force; English without a provider. */
export function useLang(): Lang {
  return useContext(LangContext);
}

/** `pick('en', 'Healthy', 'Saludable')`: the text of the language, for code that is not a component. */
export function pick(lang: Lang, en: string, es: string): string {
  return lang === 'es' ? es : en;
}
