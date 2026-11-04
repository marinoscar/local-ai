import type { Lang } from "../i18n/i18n";
import { safeReaderSrc } from "./reader";

// kiwix-serve's OpenSearch RSS (companion-services.md section 4.1.3), parsed without the network
// and without a DOM so it is unit-tested in plain Node. The parser is deliberately forgiving: a
// missing totalResults falls back to the item count, and anything that is not RSS is reported as
// "html" so the caller can show the page itself in the reader.

export interface SearchItem {
  title: string;
  /** Same-origin path under /library/ (checked with safeReaderSrc). */
  url: string;
  book: string;
  snippet: string;
}

export type ParsedSearch =
  | { kind: "results"; total: number; items: SearchItem[] }
  | { kind: "html" };

const ENTITIES: Record<string, string> = {
  amp: "&",
  lt: "<",
  gt: ">",
  quot: '"',
  apos: "'",
  nbsp: " ",
};

function decode(s: string): string {
  return s
    .replace(/<!\[CDATA\[([\s\S]*?)\]\]>/g, (_m, c: string) => c)
    .replace(/&(#x[0-9a-f]+|#\d+|\w+);/gi, (m, e: string) => {
      if (e[0] === "#") {
        const code =
          e[1].toLowerCase() === "x"
            ? parseInt(e.slice(2), 16)
            : parseInt(e.slice(1), 10);
        return Number.isFinite(code) && code > 0 && code < 0x110000
          ? String.fromCodePoint(code)
          : m;
      }
      return ENTITIES[e.toLowerCase()] ?? m;
    });
}

function tag(src: string, name: string): string | null {
  const m = new RegExp(
    `<${name}(?:\\s[^>]*)?>([\\s\\S]*?)</${name}>`,
    "i",
  ).exec(src);
  return m ? decode(m[1]).trim() : null;
}

function plain(s: string): string {
  return s
    .replace(/<[^>]*>/g, " ")
    .replace(/\s+/g, " ")
    .trim();
}

/** parseOpenSearch parses the body of /library/search?format=xml. */
export function parseOpenSearch(body: string): ParsedSearch {
  if (!/<rss[\s>]/i.test(body)) return { kind: "html" };
  const items: SearchItem[] = [];
  for (const m of body.matchAll(/<item(?:\s[^>]*)?>([\s\S]*?)<\/item>/gi)) {
    const raw = m[1];
    const url = safeReaderSrc(tag(raw, "link") ?? "");
    if (!url) continue;
    const bookBlock = /<book(?:\s[^>]*)?>([\s\S]*?)<\/book>/i.exec(raw);
    items.push({
      title: plain(tag(raw, "title") ?? "") || url,
      url,
      book: bookBlock ? plain(tag(bookBlock[1], "title") ?? "") : "",
      snippet: plain(tag(raw, "description") ?? ""),
    });
  }
  const t = Number(tag(body, "totalResults"));
  const total = Number.isFinite(t) && t >= items.length ? t : items.length;
  return { kind: "results", total, items };
}

/** langFilter maps the UI language to kiwix's book language code. */
export function langFilter(lang: Lang): "eng" | "spa" {
  return lang === "es" ? "spa" : "eng";
}

export const PAGE_LENGTH = 25;

/** searchURL is the XML request; `allLanguages` leaves the language filter out. */
export function searchURL(
  q: string,
  lang: Lang,
  allLanguages: boolean,
): string {
  const p = new URLSearchParams({ pattern: q });
  if (!allLanguages) p.set("books.filter.lang", langFilter(lang));
  p.set("format", "xml");
  p.set("pageLength", String(PAGE_LENGTH));
  return `/library/search?${p.toString()}`;
}

/** htmlSearchURL is the same search as kiwix's own results page (the fallback shown in the reader). */
export function htmlSearchURL(
  q: string,
  lang: Lang,
  allLanguages: boolean,
): string {
  return searchURL(q, lang, allLanguages).replace("&format=xml", "");
}

/** bookURL is the undated alias of a book, so links survive ZIM updates. */
export function bookURL(book: string): string {
  return `/library/content/${encodeURIComponent(book)}/`;
}
