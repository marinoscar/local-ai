import { useLang, type Lang } from '../lang';

/** A string table in both languages. The Spanish table must have every English key (the type and a test check it). */
export interface Strings<K extends string> {
  en: Record<K, string>;
  es: Record<K, string>;
}

/** fmt substitutes `{name}` placeholders; an unknown placeholder stays as it is. */
export function fmt(s: string, vars?: Record<string, string | number>): string {
  if (!vars) return s;
  return s.replace(/\{(\w+)\}/g, (m, name: string) => (name in vars ? String(vars[name]) : m));
}

/** The translate function of a table for one language. */
export function translator<K extends string>(table: Strings<K>, lang: Lang) {
  return (key: K, vars?: Record<string, string | number>): string => fmt(table[lang][key] ?? table.en[key] ?? key, vars);
}

/** useStrings gives a component its words in the language of the nearest LangProvider. */
export function useStrings<K extends string>(table: Strings<K>) {
  return translator(table, useLang());
}
