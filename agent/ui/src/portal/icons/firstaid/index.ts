// One icon per first-aid key (the key is the file name). They are emitted as files under
// /portal/assets (never inlined) and shown with <img>, which the Portal CSP allows (img-src 'self').
const files = import.meta.glob("./*.svg", {
  eager: true,
  query: "?url",
  import: "default",
}) as Record<string, string>;

/** firstAidIcon is the URL of the icon of a key, or null for a key without an icon. */
export function firstAidIcon(key: string): string | null {
  return files[`./${key}.svg`] ?? null;
}
