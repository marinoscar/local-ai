// Per-file line diffs between two catalogs, and the one-line summary the approve dialog shows
// ("2 files changed, +14 -3 lines"). Built from the full file contents of both versions; the API's `?diff=approved`
// names which version is "approved" and the digests, the lines are computed here (lineDiff.ts).
import type { CatalogFile } from '../api/catalog';
import { compareCatalogPaths } from './catalogRules';
import { unifiedHunks, type Hunk } from './lineDiff';

export type FileChange = 'added' | 'removed' | 'changed' | 'unchanged';

export interface FileLineChanges {
  path: string;
  change: FileChange;
  added: number;
  removed: number;
  hunks: Hunk[];
}

/** One entry per path of either catalog, in catalog order (CATALOG_VERSION, models, ark, profiles). */
export function diffCatalogFiles(base: readonly CatalogFile[], target: readonly CatalogFile[]): FileLineChanges[] {
  const before = new Map(base.map((file) => [file.path, file.content]));
  const after = new Map(target.map((file) => [file.path, file.content]));
  const paths = [...new Set([...before.keys(), ...after.keys()])].sort(compareCatalogPaths);
  return paths.map((path) => {
    const old = before.get(path);
    const next = after.get(path);
    const change: FileChange = old === undefined ? 'added' : next === undefined ? 'removed' : old === next ? 'unchanged' : 'changed';
    const { hunks, added, removed } = unifiedHunks(old ?? '', next ?? '');
    return { path, change, added, removed, hunks };
  });
}

export interface DiffSummary {
  filesChanged: number;
  added: number;
  removed: number;
}

export function summarizeDiff(files: readonly FileLineChanges[]): DiffSummary {
  const changed = files.filter((file) => file.change !== 'unchanged');
  return {
    filesChanged: changed.length,
    added: changed.reduce((sum, file) => sum + file.added, 0),
    removed: changed.reduce((sum, file) => sum + file.removed, 0),
  };
}

/** "2 files changed, +14 -3 lines" / "No changes". */
export function summaryText(summary: DiffSummary): string {
  if (summary.filesChanged === 0) return 'No changes';
  const files = `${summary.filesChanged} ${summary.filesChanged === 1 ? 'file' : 'files'} changed`;
  const lines = summary.added + summary.removed === 1 ? 'line' : 'lines';
  return `${files}, +${summary.added} -${summary.removed} ${lines}`;
}
