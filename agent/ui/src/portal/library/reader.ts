// The reader frame's rules (survival-portal.md section 4.1.5): which paths it may show and how a
// Portal path carries them. Pure functions, so they are unit-tested without a browser.

/** The only places the iframe may load from: the agent's own proxies and docs. */
export const READER_PREFIXES = ["/library/", "/maps/", "/docs/"] as const;

// eslint-disable-next-line no-control-regex
const BAD_CHARS = /[\\\u0000-\u001f\u007f]/;

/**
 * safeReaderSrc returns src when it is a same-origin path under /library/, /maps/ or /docs/;
 * otherwise null. It refuses absolute URLs, protocol-relative URLs, backslashes, control
 * characters and any dot segment, so the query cannot become an open redirect.
 */
export function safeReaderSrc(src: string | null | undefined): string | null {
  if (!src || !src.startsWith("/") || src.startsWith("//")) return null;
  if (BAD_CHARS.test(src)) return null;
  if (!READER_PREFIXES.some((p) => src.startsWith(p))) return null;
  const path = src.split(/[?#]/, 1)[0];
  let decoded: string;
  try {
    decoded = decodeURIComponent(path);
  } catch {
    return null;
  }
  if (decoded.includes("\\")) return null;
  if (decoded.split("/").some((seg) => seg === ".." || seg === "."))
    return null;
  return src;
}

/** safeBack returns a Portal route (/portal, /portal/...) or '/portal' for anything else. */
export function safeBack(back: string | null | undefined): string {
  if (
    back &&
    (back === "/portal" ||
      back.startsWith("/portal/") ||
      back.startsWith("/portal?")) &&
    !BAD_CHARS.test(back)
  )
    return back;
  return "/portal";
}

/** readerPath is the Portal route that opens src in the reader, coming back to `back`. */
export function readerPath(src: string, back: string): string {
  return `/portal/read?src=${encodeURIComponent(src)}&back=${encodeURIComponent(back)}`;
}

export interface ReaderRequest {
  src: string;
  back: string;
}

/** parseReaderSearch reads ?src=&back= from location.search; null when src is not allowed. */
export function parseReaderSearch(search: string): ReaderRequest | null {
  const q = new URLSearchParams(search);
  const src = safeReaderSrc(q.get("src"));
  if (!src) return null;
  return { src, back: safeBack(q.get("back")) };
}

/** isLibraryBack is true when the back route is the library screen (the button says "Back to results"). */
export function isLibraryBack(back: string): boolean {
  return back === "/portal/library" || back.startsWith("/portal/library?");
}
