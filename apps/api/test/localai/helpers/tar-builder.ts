// A small tar.gz writer for the catalog tests: the shape GitHub's release archives have (a pax global header, one
// top-level directory) plus the hostile shapes the importer must refuse (absolute paths, `..`, links).
// Deterministic: fixed mtime and owner, so the same entries give the same bytes.
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { gzipSync } from 'node:zlib';

export interface TarEntry {
  path: string;
  type?: 'file' | 'dir' | 'symlink' | 'hardlink';
  content?: string | Buffer;
  linkTarget?: string;
  /** Write the path in a pax extended header (and a truncated name), as GitHub does for long paths. */
  pax?: boolean;
}

const BLOCK = 512;

function put(header: Buffer, value: string, start: number, length: number): void {
  header.write(value.slice(0, length), start, 'utf8');
}

function octal(value: number, length: number): string {
  return value.toString(8).padStart(length - 1, '0');
}

function header(name: string, typeflag: string, size: number, linkName = ''): Buffer {
  const buf = Buffer.alloc(BLOCK);
  put(buf, name, 0, 100);
  put(buf, octal(typeflag === '5' ? 0o755 : 0o644, 8), 100, 8);
  put(buf, octal(0, 8), 108, 8);
  put(buf, octal(0, 8), 116, 8);
  put(buf, octal(size, 12), 124, 12);
  put(buf, octal(0, 12), 136, 12);
  buf.fill(0x20, 148, 156);
  buf.write(typeflag, 156, 'ascii');
  put(buf, linkName, 157, 100);
  buf.write('ustar\u000000', 257, 'binary');
  let sum = 0;
  for (const byte of buf) sum += byte;
  buf.write(`${octal(sum, 7)}\u0000 `, 148, 'binary');
  return buf;
}

function body(data: Buffer): Buffer {
  const padded = Buffer.alloc(Math.ceil(data.length / BLOCK) * BLOCK);
  data.copy(padded);
  return padded;
}

function paxRecord(key: string, value: string): Buffer {
  const rest = ` ${key}=${value}\n`;
  let length = Buffer.byteLength(rest) + 1;
  while (String(length).length + Buffer.byteLength(rest) !== length) length = String(length).length + Buffer.byteLength(rest);
  return Buffer.from(`${length}${rest}`);
}

export function buildTar(entries: readonly TarEntry[], options: { globalHeader?: boolean } = {}): Buffer {
  const parts: Buffer[] = [];
  if (options.globalHeader) {
    const pax = paxRecord('comment', '0000000000000000000000000000000000000000');
    parts.push(header('pax_global_header', 'g', pax.length), body(pax));
  }
  for (const entry of entries) {
    const type = entry.type ?? 'file';
    const data = typeof entry.content === 'string' ? Buffer.from(entry.content, 'utf8') : (entry.content ?? Buffer.alloc(0));
    const flag = type === 'dir' ? '5' : type === 'symlink' ? '2' : type === 'hardlink' ? '1' : '0';
    const size = type === 'file' ? data.length : 0;
    let name = type === 'dir' && !entry.path.endsWith('/') ? `${entry.path}/` : entry.path;
    if (entry.pax) {
      const pax = paxRecord('path', name);
      parts.push(header('PaxHeader', 'x', pax.length), body(pax));
      name = name.slice(-90);
    }
    parts.push(header(name, flag, size, entry.linkTarget ?? ''));
    if (size > 0) parts.push(body(data));
  }
  parts.push(Buffer.alloc(BLOCK * 2));
  return Buffer.concat(parts);
}

export function buildTarGz(entries: readonly TarEntry[], options: { globalHeader?: boolean } = {}): Buffer {
  return gzipSync(buildTar(entries, options), { level: 9 });
}

/** The repository's `catalog/` directory (the real files: the import parses them, so the tests do too). */
export function repoCatalogDir(): string {
  let dir = __dirname;
  for (let i = 0; i < 12; i += 1) {
    const candidate = join(dir, 'catalog');
    if (existsSync(join(candidate, 'CATALOG_VERSION')) && existsSync(join(candidate, 'models.yaml'))) return candidate;
    dir = dirname(dir);
  }
  throw new Error('the repository catalog/ directory was not found');
}

/** The files of the repository catalog as `path -> bytes`. */
export function repoCatalogFiles(): Map<string, Buffer> {
  const root = repoCatalogDir();
  const files = new Map<string, Buffer>();
  for (const name of ['CATALOG_VERSION', 'models.yaml', 'ark.yaml']) files.set(name, readFileSync(join(root, name)));
  for (const name of readdirSync(join(root, 'profiles')).sort()) files.set(`profiles/${name}`, readFileSync(join(root, 'profiles', name)));
  return files;
}

export interface CatalogArchiveOptions {
  /** The top-level directory (GitHub: `<repo>-<tag>`). */
  top?: string;
  /** Replace the content of a catalog file, or drop it with `null`. */
  override?: Record<string, string | null>;
  /** Extra entries appended after the catalog (hostile ones, for the refusal tests). */
  extra?: TarEntry[];
}

/** A release archive of the repository at a tag: other directories, a symlink outside `catalog/`, and the catalog. */
export function buildCatalogArchive(options: CatalogArchiveOptions = {}): Buffer {
  const top = options.top ?? 'local-ai-catalog-fixture';
  const entries: TarEntry[] = [
    { path: `${top}/`, type: 'dir' },
    { path: `${top}/README.md`, content: '# fixture\n' },
    { path: `${top}/docs/`, type: 'dir' },
    { path: `${top}/docs/guide.md`, content: 'docs travel in the same archive\n' },
    // The repository has symlinks outside catalog/; they must not break an import.
    { path: `${top}/apps/cloud/packages/ui-link`, type: 'symlink', linkTarget: '../../../packages/ui' },
    { path: `${top}/catalog/`, type: 'dir' },
    { path: `${top}/catalog/profiles/`, type: 'dir' },
  ];
  for (const [path, bytes] of repoCatalogFiles()) {
    const override = options.override?.[path];
    if (override === null) continue;
    entries.push({ path: `${top}/catalog/${path}`, content: override ?? bytes });
  }
  entries.push(...(options.extra ?? []));
  return buildTarGz(entries, { globalHeader: true });
}
