// File-level difference between two catalogs (`GET /versions/:version?diff=approved`, docs/specs/cloud-app.md 3.9).
// Line-level rendering is the editor UI's job (CLOUD-13); the API tells it which files changed and by which digests.
import type { CatalogFile } from './catalog.constants';
import { compareCatalogPaths } from './bundle-hash';

export type FileChange = 'added' | 'removed' | 'changed' | 'unchanged';

export interface FileDiff {
  path: string;
  change: FileChange;
  /** The digest in the compared (approved) catalog; null when the file is new. */
  baseSha256: string | null;
  /** The digest in this catalog; null when the file was removed. */
  sha256: string | null;
}

export function diffCatalogs(base: ReadonlyArray<Pick<CatalogFile, 'path' | 'sha256'>>, next: ReadonlyArray<Pick<CatalogFile, 'path' | 'sha256'>>): FileDiff[] {
  const before = new Map(base.map((file) => [file.path, file.sha256]));
  const after = new Map(next.map((file) => [file.path, file.sha256]));
  const paths = [...new Set([...before.keys(), ...after.keys()])].sort(compareCatalogPaths);
  return paths.map((path) => {
    const baseSha256 = before.get(path) ?? null;
    const sha256 = after.get(path) ?? null;
    const change: FileChange = baseSha256 === null ? 'added' : sha256 === null ? 'removed' : baseSha256 === sha256 ? 'unchanged' : 'changed';
    return { path, change, baseSha256, sha256 };
  });
}
