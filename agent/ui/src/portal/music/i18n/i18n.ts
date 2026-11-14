import { getLang, type Lang, type Vars } from "../../i18n/i18n";
import en from "./en.json";
import es from "./es.json";

// The words of the /music/ page (issue #512). They live next to the page, not in the Portal table, because the
// Portal's en.json/es.json must equal survival-portal.md section 4.9 and music is not a survival screen. The language
// is the Portal's own (same store, same toggle); the shared components get it through LangProvider.

export type MusicKey = keyof typeof en;

export const MUSIC_TABLES: Record<Lang, Record<string, string>> = { en, es };

/** mt translates a music key into the current Portal language ({name} placeholders as in the Portal). */
export function mt(key: MusicKey, vars?: Vars): string {
  const raw = MUSIC_TABLES[getLang()][key] ?? en[key] ?? key;
  if (!vars) return raw;
  return raw.replace(/\{(\w+)\}/g, (m, n: string) =>
    n in vars ? String(vars[n]) : m,
  );
}
