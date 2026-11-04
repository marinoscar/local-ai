// Per-file digests and the bundle digest (docs/specs/cloud-app.md section 3.6).
//
// `bundleSha256` is the sha256 over the lines `"<path>\0<sha256>\n"` sorted bytewise: the same function as the
// agent's `catalog.BundleDigest` (`agent/internal/catalog/fetch_github.go`), so one tag has one digest whether the
// cloud imported it or a node fetched it from GitHub. It is also the strong ETag of the node route.
import { createHash } from 'node:crypto';

import type { CatalogFile } from './catalog.constants';

export function sha256Hex(data: string | Uint8Array): string {
  return createHash('sha256').update(data).digest('hex');
}

/** The digest of a set of files: sorted `"path\0sha256\n"` lines. Order of the input does not matter. */
export function bundleSha256(files: ReadonlyArray<Pick<CatalogFile, 'path' | 'sha256'>>): string {
  const lines = files.map((file) => `${file.path}\u0000${file.sha256.toLowerCase()}\n`).sort();
  const hash = createHash('sha256');
  for (const line of lines) hash.update(line);
  return hash.digest('hex');
}

/** The order files are stored and served in: `CATALOG_VERSION`, `models.yaml`, `ark.yaml`, then profiles by name. */
export function compareCatalogPaths(a: string, b: string): number {
  const rank = (path: string): number => (path === 'CATALOG_VERSION' ? 0 : path === 'models.yaml' ? 1 : path === 'ark.yaml' ? 2 : 3);
  return rank(a) - rank(b) || (a < b ? -1 : a > b ? 1 : 0);
}

/** Files with their digests, in canonical order. `content` is hashed as UTF-8, which is what the agent hashes. */
export function toCatalogFiles(entries: ReadonlyArray<{ path: string; content: string }>): CatalogFile[] {
  return entries
    .map((entry) => ({ path: entry.path, sha256: sha256Hex(Buffer.from(entry.content, 'utf8')), content: entry.content }))
    .sort((a, b) => compareCatalogPaths(a.path, b.path));
}
