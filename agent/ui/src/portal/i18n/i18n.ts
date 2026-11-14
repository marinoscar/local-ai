import { useSyncExternalStore } from "react";
import en from "./en.json";
import es from "./es.json";
import { MANAGE_EN, MANAGE_ES } from "./manage";

// Translation helper of the Portal (survival-portal.md sections 4.3 and 4.9). Every user-visible
// string is a key of en.json / es.json; a lint rule forbids JSX text literals.

export type Lang = "en" | "es";
export type Key = keyof typeof en | keyof typeof MANAGE_EN;
export type Vars = Record<string, string | number>;

export const LANG_KEY = "lai.portal.lang";

const tables: Record<Lang, Record<string, string>> = {
  en: { ...en, ...MANAGE_EN },
  es: { ...es, ...MANAGE_ES },
};

export function isLang(v: unknown): v is Lang {
  return v === "en" || v === "es";
}

export interface LangSources {
  /** localStorage value of lai.portal.lang. */
  stored?: string | null;
  /** status.language: the agent applied portal.language / the profile. */
  status?: string | null;
  /** navigator.language, e.g. "es-MX". */
  navigator?: string | null;
}

/**
 * resolveLang is the initial language, in order: the stored choice, the language the agent reports,
 * the browser language, then English.
 */
export function resolveLang(src: LangSources): Lang {
  if (isLang(src.stored)) return src.stored;
  if (isLang(src.status)) return src.status;
  const nav = (src.navigator ?? "").toLowerCase().split("-")[0];
  if (isLang(nav)) return nav;
  return "en";
}

/** translate substitutes {name} placeholders. A missing key shows the key itself (never blank). */
export function translate(lang: Lang, key: string, vars?: Vars): string {
  const raw = tables[lang][key] ?? tables.en[key] ?? key;
  if (!vars) return raw;
  return raw.replace(/\{(\w+)\}/g, (m, name: string) =>
    name in vars ? String(vars[name]) : m,
  );
}

let current: Lang = "en";
let chosen = false;
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

function readStored(): string | null {
  try {
    return window.localStorage.getItem(LANG_KEY);
  } catch {
    return null;
  }
}

/** getLang is the current language (used by the API wrapper for X-LocalAI-UI-Language). */
export function getLang(): Lang {
  return current;
}

/** t translates into the current language. */
export function t(key: Key, vars?: Vars): string {
  return translate(current, key, vars);
}

/** initLang resolves the language once at start-up. The status language is applied later by applyStatusLang. */
export function initLang(): void {
  chosen = isLang(readStored());
  current = resolveLang({
    stored: readStored(),
    navigator: typeof navigator === "undefined" ? null : navigator.language,
  });
  document.documentElement.lang = current;
  emit();
}

/** applyStatusLang uses the agent's language until the person has made a choice. */
export function applyStatusLang(statusLang: string | undefined): void {
  if (chosen || !isLang(statusLang) || statusLang === current) return;
  current = resolveLang({ stored: null, status: statusLang });
  document.documentElement.lang = current;
  emit();
}

/** setLang is the toggle: instant, and remembered. */
export function setLang(lang: Lang): void {
  chosen = true;
  current = lang;
  try {
    window.localStorage.setItem(LANG_KEY, lang);
  } catch {
    // Storage blocked: the choice lasts until reload.
  }
  document.documentElement.lang = lang;
  emit();
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

/** useLang re-renders the component when the language changes. */
export function useLang(): Lang {
  return useSyncExternalStore(subscribe, getLang);
}
