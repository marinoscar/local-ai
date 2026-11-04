// The catalog rules the editor mirrors from the server (docs/specs/cloud-app.md 4.6; `apps/api/src/localai/catalog/`):
// the path rule, the version format and order, the size limit. The server enforces them; the page mirrors them so a
// refusal is explained before the request, never invented here: if the two ever disagree, the server wins and its
// message is shown.

/** The only paths a catalog may carry (the API's and the agent's regex). */
export const CATALOG_PATH_RE = /^(CATALOG_VERSION|models\.yaml|ark\.yaml|profiles\/[a-z0-9-]+\.yaml)$/;

/** The most a catalog may weigh in total. */
export const MAX_CATALOG_BYTES = 2 * 1024 * 1024;

/** The name of the file the server rewrites from the version field. */
export const VERSION_FILE = 'CATALOG_VERSION';

export function isCatalogPath(path: string): boolean {
  return CATALOG_PATH_RE.test(path);
}

/** `profiles/<name>.yaml` for a profile name, or null when the name makes a path the server would refuse. */
export function profilePath(name: string): string | null {
  const path = `profiles/${name.trim()}.yaml`;
  return CATALOG_PATH_RE.test(path) && path.startsWith('profiles/') ? path : null;
}

/** A version: a real calendar date and a counter of 1 or more with no leading zero (at most 9 digits). */
const VERSION_RE = /^(\d{4})\.(\d{2})\.(\d{2})-([1-9]\d{0,8})$/;

export interface ParsedVersion {
  year: number;
  month: number;
  day: number;
  n: number;
}

export function parseVersion(version: string): ParsedVersion | null {
  const match = VERSION_RE.exec(version);
  if (!match) return null;
  const [year, month, day, n] = [Number(match[1]), Number(match[2]), Number(match[3]), Number(match[4])];
  const date = new Date(Date.UTC(year, month - 1, day));
  if (date.getUTCFullYear() !== year || date.getUTCMonth() !== month - 1 || date.getUTCDate() !== day) return null;
  return { year, month, day, n };
}

export function isCatalogVersion(value: string): boolean {
  return parseVersion(value) !== null;
}

/** The tag a release is published under. */
export const CATALOG_TAG_RE = /^catalog-(\d{4}\.\d{2}\.\d{2}-[1-9]\d{0,8})$/;

/** The version a `catalog-<version>` tag names, or null (`catalog-stable` and malformed tags are null). */
export function versionOfTag(tag: string): string | null {
  const match = CATALOG_TAG_RE.exec(tag);
  return match && isCatalogVersion(match[1]!) ? match[1]! : null;
}

/**
 * Negative when `a` is older than `b`, positive when newer. The date part first, then N as a NUMBER, so `2026.11.02-12`
 * is greater than `2026.11.02-2` and `2026.11.03-1` is greater than both. Both must be valid versions.
 */
export function compareVersions(a: string, b: string): number {
  const left = parseVersion(a);
  const right = parseVersion(b);
  if (!left || !right) throw new Error('compareVersions: not a catalog version');
  return left.year - right.year || left.month - right.month || left.day - right.day || left.n - right.n;
}

/** True when `version` is greater than every version in `existing` (true for an empty list). */
export function isGreaterThanAll(version: string, existing: readonly string[]): boolean {
  return existing.every((other) => !isCatalogVersion(other) || compareVersions(version, other) > 0);
}

/** `YYYY.MM.DD` of a date in UTC (the release tags are dated in UTC). */
export function dateStamp(date: Date): string {
  const pad = (value: number, width: number) => String(value).padStart(width, '0');
  return `${pad(date.getUTCFullYear(), 4)}.${pad(date.getUTCMonth() + 1, 2)}.${pad(date.getUTCDate(), 2)}`;
}

/**
 * The version a new draft should start with: `<today>-1`, or the next counter of the newest version when that is today
 * or later, so the suggestion is always greater than every existing version.
 */
export function suggestNextVersion(existing: readonly string[], today: Date): string {
  const valid = existing.filter(isCatalogVersion).sort(compareVersions);
  const newest = valid[valid.length - 1];
  const candidate = `${dateStamp(today)}-1`;
  if (!newest || compareVersions(candidate, newest) > 0) return candidate;
  const parsed = parseVersion(newest)!;
  const stamp = newest.slice(0, newest.indexOf('-'));
  return `${stamp}-${parsed.n + 1}`;
}

/** The UTF-8 size of a text, as the server counts it. */
export function byteLength(text: string): number {
  return new TextEncoder().encode(text).length;
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
}

/** The order of the file tree: CATALOG_VERSION, models.yaml, ark.yaml, then profiles by name. */
export function compareCatalogPaths(a: string, b: string): number {
  const rank = (path: string) => (path === VERSION_FILE ? 0 : path === 'models.yaml' ? 1 : path === 'ark.yaml' ? 2 : 3);
  return rank(a) - rank(b) || (a < b ? -1 : a > b ? 1 : 0);
}
