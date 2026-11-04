import { useSyncExternalStore } from "react";
import en from "./i18n/en.json";
import es from "./i18n/es.json";

// Language of the map viewer. It follows the Portal's choice (the same localStorage key, the viewer
// is same-origin), then ?lang=, then the browser language, then English.

export type Lang = "en" | "es";
export type Key = keyof typeof en;
export const LANG_KEY = "lai.portal.lang";

const tables: Record<Lang, Record<string, string>> = { en, es };

export function isLang(v: unknown): v is Lang {
  return v === "en" || v === "es";
}

export function translate(
  lang: Lang,
  key: string,
  vars?: Record<string, string | number>,
): string {
  const raw = tables[lang][key] ?? tables.en[key] ?? key;
  if (!vars) return raw;
  return raw.replace(/\{(\w+)\}/g, (m, n: string) =>
    n in vars ? String(vars[n]) : m,
  );
}

/** pickLang: query value, stored value, browser language, English. */
export function pickLang(
  query: string | null,
  stored: string | null,
  nav: string | null,
): Lang {
  if (isLang(query)) return query;
  if (isLang(stored)) return stored;
  const n = (nav ?? "").toLowerCase().split("-")[0];
  return isLang(n) ? n : "en";
}

let current: Lang = "en";
const listeners = new Set<() => void>();

function stored(): string | null {
  try {
    return window.localStorage.getItem(LANG_KEY);
  } catch {
    return null;
  }
}

export function initLang(): void {
  current = pickLang(
    new URLSearchParams(window.location.search).get("lang"),
    stored(),
    navigator.language,
  );
  document.documentElement.lang = current;
}

export function getLang(): Lang {
  return current;
}

export function setLang(l: Lang): void {
  current = l;
  try {
    window.localStorage.setItem(LANG_KEY, l);
  } catch {
    // Storage blocked: the choice lasts until reload.
  }
  document.documentElement.lang = l;
  listeners.forEach((f) => f());
}

export function useLang(): Lang {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => current,
  );
}

export function t(key: Key, vars?: Record<string, string | number>): string {
  return translate(current, key, vars);
}
