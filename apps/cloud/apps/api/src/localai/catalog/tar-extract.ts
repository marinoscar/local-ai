// Safe extraction of `catalog/**` from a GitHub release archive (`.tar.gz`, docs/specs/cloud-app.md section 4.6).
//
// NOTHING IS EVER WRITTEN TO DISK HERE. The archive is parsed in memory and only the allowed catalog files are kept,
// as bytes. The path rules still matter: they are what keeps a hostile or damaged archive from being accepted as a
// catalog, and they keep the paths that later reach a temp directory (the lint job) inside the catalog layout.
//
// What is refused, whole archive, no partial result:
//   - a path that is absolute, holds a `..` segment, a NUL byte or a backslash;
//   - a symlink or hardlink (or any other special entry) AT or UNDER `catalog/` (links elsewhere in the repository
//     are normal and ignored: the archive is the whole repo, and the repo holds symlinks outside `catalog/`);
//   - a file under `catalog/` that is not one of the catalog's paths (`CATALOG_PATH_RE`), or listed twice;
//   - more than `maxCatalogBytes` of catalog files, more than `maxUncompressedBytes` inflated, or too many entries
//     (a decompression bomb); a damaged gzip stream or tar header;
//   - an archive with more than one top-level directory (a GitHub archive has exactly one: `<repo>-<ref>/`).
//
// The format is the subset GitHub emits: ustar headers, a pax global header (`g`, the commit id), pax extended
// headers (`x`, long paths) and GNU long names (`L`).
import { Readable } from 'node:stream';
import { createGunzip } from 'node:zlib';

import { CATALOG_PATH_RE, MAX_ARCHIVE_ENTRIES, MAX_ARCHIVE_UNCOMPRESSED_BYTES, MAX_CATALOG_BYTES } from './catalog.constants';

export type ArchiveErrorCode =
  | 'bad_archive'
  | 'unsafe_path'
  | 'link_entry'
  | 'unexpected_file'
  | 'duplicate_file'
  | 'too_large'
  | 'no_catalog';

/** A refused archive. `code` is stable (tests and the job's log line match on it); `message` is plain language. */
export class CatalogArchiveError extends Error {
  constructor(
    readonly code: ArchiveErrorCode,
    message: string,
  ) {
    super(message);
    this.name = 'CatalogArchiveError';
  }
}

export interface ExtractOptions {
  maxCatalogBytes?: number;
  maxUncompressedBytes?: number;
  maxEntries?: number;
}

const BLOCK = 512;
const CATALOG_DIR = 'catalog';

/** Reads a NUL-terminated string field. */
function field(header: Buffer, start: number, length: number): string {
  const slice = header.subarray(start, start + length);
  const nul = slice.indexOf(0);
  return slice.subarray(0, nul === -1 ? slice.length : nul).toString('utf8');
}

/** Parses an octal numeric field; `null` when it is not octal (a base-256 size is not supported on purpose). */
function octal(header: Buffer, start: number, length: number): number | null {
  const text = field(header, start, length).trim();
  if (text === '') return 0;
  return /^[0-7]+$/.test(text) ? parseInt(text, 8) : null;
}

function checksumOk(header: Buffer): boolean {
  const stored = octal(header, 148, 8);
  if (stored === null) return false;
  let sum = 0;
  for (let i = 0; i < BLOCK; i += 1) sum += i >= 148 && i < 156 ? 0x20 : header[i];
  return sum === stored;
}

/** Records of a pax extended header: `"<len> <key>=<value>\n"`. Only the keys used here are returned. */
function parsePax(data: Buffer): { path?: string; linkpath?: string } {
  const out: { path?: string; linkpath?: string } = {};
  let offset = 0;
  while (offset < data.length) {
    const space = data.indexOf(0x20, offset);
    if (space === -1) break;
    const length = Number(data.subarray(offset, space).toString('ascii'));
    if (!Number.isInteger(length) || length <= space - offset || offset + length > data.length) {
      throw new CatalogArchiveError('bad_archive', 'The archive has a damaged extended header.');
    }
    const record = data.subarray(space + 1, offset + length - 1).toString('utf8');
    const eq = record.indexOf('=');
    if (eq > 0) {
      const key = record.slice(0, eq);
      if (key === 'path') out.path = record.slice(eq + 1);
      if (key === 'linkpath') out.linkpath = record.slice(eq + 1);
    }
    offset += length;
  }
  return out;
}

/** A tar path to its segments, or an `unsafe_path` refusal. A leading `./` is dropped. */
function safeSegments(raw: string): string[] {
  if (raw.includes('\0') || raw.includes('\\') || raw.startsWith('/') || /^[A-Za-z]:/.test(raw)) {
    throw new CatalogArchiveError('unsafe_path', 'The archive holds an absolute or unsafe path.');
  }
  const segments = raw.split('/').filter((segment, index) => !(segment === '.' && index === 0) && segment !== '');
  if (segments.some((segment) => segment === '..' || segment === '.')) {
    throw new CatalogArchiveError('unsafe_path', 'The archive holds a path that climbs out of its directory.');
  }
  return segments;
}

/** A pull-style byte reader over the inflated stream, counting what it inflates. */
class ByteReader {
  private buffer: Buffer = Buffer.alloc(0);
  private readonly iterator: AsyncIterator<Buffer>;
  inflated = 0;

  constructor(
    stream: AsyncIterable<Buffer>,
    private readonly maxInflated: number,
  ) {
    this.iterator = stream[Symbol.asyncIterator]();
  }

  private async pull(): Promise<boolean> {
    const next = await this.iterator.next();
    if (next.done) return false;
    this.inflated += next.value.length;
    if (this.inflated > this.maxInflated) {
      throw new CatalogArchiveError('too_large', 'The archive is larger than allowed once unpacked.');
    }
    this.buffer = this.buffer.length === 0 ? next.value : Buffer.concat([this.buffer, next.value]);
    return true;
  }

  /** Exactly `length` bytes, or null at a clean end of stream before any byte was read. */
  async read(length: number): Promise<Buffer | null> {
    while (this.buffer.length < length) {
      if (!(await this.pull())) {
        if (this.buffer.length === 0) return null;
        throw new CatalogArchiveError('bad_archive', 'The archive ends in the middle of an entry.');
      }
    }
    const out = this.buffer.subarray(0, length);
    this.buffer = this.buffer.subarray(length);
    return out;
  }

  async skip(length: number): Promise<void> {
    let left = length;
    while (left > 0) {
      if (this.buffer.length === 0 && !(await this.pull())) {
        throw new CatalogArchiveError('bad_archive', 'The archive ends in the middle of an entry.');
      }
      const take = Math.min(left, this.buffer.length);
      this.buffer = this.buffer.subarray(take);
      left -= take;
    }
  }

  close(stream: { destroy(): void }): void {
    stream.destroy();
  }
}

/**
 * The catalog files of a `.tar.gz` release archive: relative path (`models.yaml`, `profiles/small-8gb.yaml`) to
 * bytes. Throws `CatalogArchiveError` for anything the rules above refuse.
 */
export async function extractCatalogFiles(archive: Buffer, options: ExtractOptions = {}): Promise<Map<string, Buffer>> {
  const maxCatalogBytes = options.maxCatalogBytes ?? MAX_CATALOG_BYTES;
  const maxEntries = options.maxEntries ?? MAX_ARCHIVE_ENTRIES;
  const gunzip = createGunzip();
  const source = Readable.from([archive]).pipe(gunzip);
  const reader = new ByteReader(source, options.maxUncompressedBytes ?? MAX_ARCHIVE_UNCOMPRESSED_BYTES);

  const files = new Map<string, Buffer>();
  let catalogBytes = 0;
  let entries = 0;
  let top: string | null = null;
  let pendingPath: string | undefined;
  let pendingLink: string | undefined;

  try {
    for (;;) {
      const header = await reader.read(BLOCK);
      if (header === null || header.every((byte) => byte === 0)) break;
      entries += 1;
      if (entries > maxEntries) throw new CatalogArchiveError('too_large', 'The archive lists too many entries.');
      if (!checksumOk(header)) throw new CatalogArchiveError('bad_archive', 'The archive has a damaged header.');

      const size = octal(header, 124, 12);
      if (size === null) throw new CatalogArchiveError('bad_archive', 'The archive has an unsupported entry size.');
      const type = String.fromCharCode(header[156] || 0x30);
      const padded = Math.ceil(size / BLOCK) * BLOCK;

      // Metadata entries: read their body, remember what they say for the NEXT entry, and move on.
      if (type === 'x' || type === 'L' || type === 'K') {
        if (size > 64 * 1024) throw new CatalogArchiveError('bad_archive', 'The archive has an oversized extended header.');
        const body = (await reader.read(padded)) ?? Buffer.alloc(0);
        const data = body.subarray(0, size);
        if (type === 'x') {
          const pax = parsePax(data);
          pendingPath = pax.path ?? pendingPath;
          pendingLink = pax.linkpath ?? pendingLink;
        } else if (type === 'L') {
          pendingPath = field(data, 0, data.length);
        } else {
          pendingLink = field(data, 0, data.length);
        }
        continue;
      }
      if (type === 'g') {
        // The pax GLOBAL header (GitHub writes the commit id here): no path, applies to nothing we keep.
        await reader.skip(padded);
        continue;
      }

      let name = pendingPath;
      if (name === undefined) {
        const prefix = header.toString('ascii', 257, 262) === 'ustar' ? field(header, 345, 155) : '';
        name = prefix ? `${prefix}/${field(header, 0, 100)}` : field(header, 0, 100);
      }
      pendingPath = undefined;
      pendingLink = undefined;

      // Every entry's path is checked, kept or not: a hostile archive is refused whole.
      const segments = safeSegments(name);
      if (segments.length === 0) {
        await reader.skip(padded);
        continue;
      }
      if (top === null) top = segments[0];
      else if (segments[0] !== top) {
        throw new CatalogArchiveError('bad_archive', 'The archive does not have a single top-level directory.');
      }
      const inside = segments.slice(1);
      const underCatalog = inside[0] === CATALOG_DIR;

      if (!underCatalog) {
        await reader.skip(padded);
        continue;
      }
      if (type === '5') {
        await reader.skip(padded);
        continue;
      }
      if (type !== '0' && type !== '7') {
        // A symlink, hardlink, device or fifo at or under catalog/.
        throw new CatalogArchiveError('link_entry', 'The catalog holds a link or special file, which is not allowed.');
      }

      const relative = inside.slice(1).join('/');
      if (!CATALOG_PATH_RE.test(relative)) {
        throw new CatalogArchiveError('unexpected_file', `The catalog holds a file outside its layout: ${relative.slice(0, 80)}`);
      }
      if (files.has(relative)) throw new CatalogArchiveError('duplicate_file', `The catalog lists ${relative} twice.`);
      catalogBytes += size;
      if (catalogBytes > maxCatalogBytes) throw new CatalogArchiveError('too_large', 'The catalog is larger than 2 MB.');

      const body = size === 0 ? Buffer.alloc(0) : await reader.read(padded);
      if (body === null) throw new CatalogArchiveError('bad_archive', 'The archive ends in the middle of an entry.');
      files.set(relative, Buffer.from(body.subarray(0, size)));
    }
  } catch (error) {
    if (error instanceof CatalogArchiveError) throw error;
    // zlib errors (bad header, truncated stream, corrupt data) and anything unexpected from the stream.
    throw new CatalogArchiveError('bad_archive', 'The archive is not a valid .tar.gz file.');
  } finally {
    reader.close(source);
  }

  if (files.size === 0) throw new CatalogArchiveError('no_catalog', 'The archive has no catalog/ directory with catalog files.');
  return files;
}
