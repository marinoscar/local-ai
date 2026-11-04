// Catalog versions: `YYYY.MM.DD-N` (docs/specs/catalog.md section 1, the version rule of the release workflow).
//
// Compared field by field as NUMBERS, never as strings: `2026.11.02-10` is greater than `2026.11.02-9`. The rule is
// the one `scripts/catalog-version-guard.sh` enforces on tags, so the cloud and the tagger can never disagree on
// which version is newer.

/** A version: a real calendar date and a counter of 1 or more with no leading zero (at most 9 digits). */
const VERSION_RE = /^(\d{4})\.(\d{2})\.(\d{2})-([1-9]\d{0,8})$/;

/** The tag prefix of a catalog release. */
const TAG_PREFIX = 'catalog-';

export interface ParsedCatalogVersion {
  year: number;
  month: number;
  day: number;
  n: number;
}

/** The fields of a version, or null when it is not `YYYY.MM.DD-N` with a real date. */
export function parseCatalogVersion(version: string): ParsedCatalogVersion | null {
  const match = VERSION_RE.exec(version);
  if (!match) return null;
  const [year, month, day, n] = [Number(match[1]), Number(match[2]), Number(match[3]), Number(match[4])];
  // A real date: round-trip through UTC so 2026.02.30 is refused.
  const date = new Date(Date.UTC(year, month - 1, day));
  if (date.getUTCFullYear() !== year || date.getUTCMonth() !== month - 1 || date.getUTCDate() !== day) return null;
  return { year, month, day, n };
}

export function isCatalogVersion(value: string): boolean {
  return parseCatalogVersion(value) !== null;
}

/** Negative when `a` is older than `b`, positive when newer, 0 when equal. Both must be valid versions. */
export function compareCatalogVersions(a: string, b: string): number {
  const left = parseCatalogVersion(a);
  const right = parseCatalogVersion(b);
  if (!left || !right) throw new Error('compareCatalogVersions: not a catalog version');
  return left.year - right.year || left.month - right.month || left.day - right.day || left.n - right.n;
}

/** True when `version` is greater than every version in `existing` (true for an empty list). */
export function isGreaterThanAll(version: string, existing: readonly string[]): boolean {
  return existing.every((other) => !isCatalogVersion(other) || compareCatalogVersions(version, other) > 0);
}

/** The version a `catalog-<version>` tag names, or null (`catalog-stable` and malformed tags are null). */
export function versionOfCatalogTag(tag: string): string | null {
  if (!tag.startsWith(TAG_PREFIX)) return null;
  const version = tag.slice(TAG_PREFIX.length);
  return isCatalogVersion(version) ? version : null;
}

export function catalogTagOf(version: string): string {
  return `${TAG_PREFIX}${version}`;
}
