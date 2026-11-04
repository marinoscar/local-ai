// Turning the two kinds of input into stored catalog files: the files of a release archive (import) and the files an
// administrator sends (editor or upload). Both end as `CatalogFile[]` in canonical order with their digests.
//
// Pure functions. A refusal is a `CatalogValidationError` whose `kind` the callers map to an HTTP status (the routes)
// or to a job failure (the import job).
import { CATALOG_PATH_RE, MAX_CATALOG_BYTES, type CatalogFile } from './catalog.constants';
import { toCatalogFiles } from './bundle-hash';
import { isCatalogVersion } from './version';

export type CatalogValidationKind =
  | 'bad_path'
  | 'duplicate_path'
  | 'too_large'
  | 'bad_encoding'
  | 'bad_version'
  | 'version_mismatch'
  | 'missing_version_file';

export class CatalogValidationError extends Error {
  constructor(
    readonly kind: CatalogValidationKind,
    message: string,
  ) {
    super(message);
    this.name = 'CatalogValidationError';
  }
}

const VERSION_FILE = 'CATALOG_VERSION';

/** UTF-8 text without NUL (JSONB cannot store U+0000) and without invalid sequences. A BOM is kept as it is. */
export function decodeCatalogText(bytes: Uint8Array, path: string): string {
  let text: string;
  try {
    text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes);
  } catch {
    throw new CatalogValidationError('bad_encoding', `${path} is not valid UTF-8 text.`);
  }
  if (text.includes('\u0000')) throw new CatalogValidationError('bad_encoding', `${path} contains a NUL character.`);
  return text;
}

function byteLength(text: string): number {
  return Buffer.byteLength(text, 'utf8');
}

/** The version a `CATALOG_VERSION` file holds: one line, `<version>` with an optional trailing newline. */
export function versionOfVersionFile(content: string): string {
  const version = content.replace(/\r?\n$/, '');
  if (!isCatalogVersion(version)) {
    throw new CatalogValidationError('bad_version', 'CATALOG_VERSION must be one line of the form YYYY.MM.DD-N.');
  }
  return version;
}

/**
 * The files of an extracted release archive. `expectedVersion` is the tag's version: `CATALOG_VERSION` must equal it
 * (catalog.md section 1). The content is stored exactly as it is in the archive, so the digests equal the ones a node
 * computes after fetching the same tag from GitHub.
 */
export function filesFromArchive(extracted: ReadonlyMap<string, Uint8Array>, expectedVersion: string): { version: string; files: CatalogFile[] } {
  const versionBytes = extracted.get(VERSION_FILE);
  if (!versionBytes) throw new CatalogValidationError('missing_version_file', 'The catalog has no CATALOG_VERSION file.');
  const entries: Array<{ path: string; content: string }> = [];
  let total = 0;
  for (const [path, bytes] of extracted) {
    if (!CATALOG_PATH_RE.test(path)) throw new CatalogValidationError('bad_path', `${path.slice(0, 80)} is not a catalog path.`);
    total += bytes.length;
    entries.push({ path, content: decodeCatalogText(bytes, path) });
  }
  if (total > MAX_CATALOG_BYTES) throw new CatalogValidationError('too_large', 'The catalog is larger than 2 MB.');
  const version = versionOfVersionFile(entries.find((entry) => entry.path === VERSION_FILE)?.content ?? '');
  if (version !== expectedVersion) {
    throw new CatalogValidationError('version_mismatch', `CATALOG_VERSION says ${version} but the tag names ${expectedVersion}.`);
  }
  return { version, files: toCatalogFiles(entries) };
}

/**
 * The files of an editor or upload draft. Paths must be catalog paths and unique; `CATALOG_VERSION` is written (or
 * overwritten) with the new `version`; the total, with that file, is at most 2 MB.
 */
export function filesFromEditor(input: ReadonlyArray<{ path: string; content: string }>, version: string): CatalogFile[] {
  const seen = new Set<string>();
  const entries: Array<{ path: string; content: string }> = [];
  for (const file of input) {
    if (!CATALOG_PATH_RE.test(file.path)) {
      throw new CatalogValidationError('bad_path', `${file.path.slice(0, 80)} is not an allowed catalog path.`);
    }
    if (seen.has(file.path)) throw new CatalogValidationError('duplicate_path', `${file.path} is listed twice.`);
    seen.add(file.path);
    if (file.path === VERSION_FILE) continue; // rewritten below
    if (file.content.includes('\u0000')) throw new CatalogValidationError('bad_encoding', `${file.path} contains a NUL character.`);
    entries.push({ path: file.path, content: file.content });
  }
  entries.push({ path: VERSION_FILE, content: `${version}\n` });
  const total = entries.reduce((sum, entry) => sum + byteLength(entry.content), 0);
  if (total > MAX_CATALOG_BYTES) throw new CatalogValidationError('too_large', 'The catalog is larger than 2 MB.');
  return toCatalogFiles(entries);
}

/** The bytes an input would weigh, for a cheap early check before any hashing. */
export function editorPayloadBytes(input: ReadonlyArray<{ content: string }>): number {
  return input.reduce((sum, file) => sum + byteLength(file.content), 0);
}
