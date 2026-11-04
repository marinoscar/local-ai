// The data hooks of the Catalog page: the versions list, one version, and a version diffed against another.
import { useEffect, useMemo, useRef, useState } from 'react';

import type { CatalogApi, CatalogFile, CatalogVersionDetail, CatalogVersionSummary } from '../api/catalog';
import { diffCatalogFiles, summarizeDiff, type DiffSummary, type FileLineChanges } from './catalogDiff';
import { usePolled, type PolledState } from './usePolled';

/** A visible page refreshes every 5 s (spec 3.11); every 2 s while a lint is being waited for. */
export const LIST_POLL_MS = 5000;
export const LINT_POLL_MS = 2000;

/** True while a verdict is still to come: never linted, or a re-lint was asked for and has not landed yet. */
export function isChecking(version: CatalogVersionSummary, pending: Readonly<Record<string, string | null>>): boolean {
  if (version.status === 'approved' || version.status === 'superseded') return false;
  if (version.lintedAt === null) return true;
  return version.version in pending && pending[version.version] === version.lintedAt;
}

export function useCatalogVersions(client: CatalogApi, pending: Readonly<Record<string, string | null>>): PolledState<CatalogVersionSummary[]> {
  return usePolled<CatalogVersionSummary[]>(
    'versions',
    () => client.list(),
    (data) => (data?.some((version) => isChecking(version, pending)) ? LINT_POLL_MS : LIST_POLL_MS),
  );
}

/**
 * One version with its files and lint result. Polls the version itself every 2 s while `lintedAt` is null (the lint
 * job has not written a verdict), and reloads when `reloadToken` changes (the list saw a new verdict).
 */
export function useCatalogVersion(client: CatalogApi, version: string | null, reloadToken: unknown = null): PolledState<CatalogVersionDetail> {
  return usePolled<CatalogVersionDetail>(
    version,
    () => client.get(version!),
    (data) => (data && data.lintedAt === null && data.status !== 'approved' && data.status !== 'superseded' ? LINT_POLL_MS : null),
    reloadToken,
  );
}

export interface CatalogDiffState {
  loading: boolean;
  error: Error | null;
  /** The version diffed against (null when there is nothing to compare with). */
  against: string | null;
  files: FileLineChanges[];
  summary: DiffSummary;
}

const EMPTY_SUMMARY: DiffSummary = { filesChanged: 0, added: 0, removed: 0 };

/**
 * `target` against the approved version (`base = 'approved'`, the API's `?diff=approved` names it) or against a
 * given version. With nothing to compare against, every file of `target` is an addition.
 */
export function useCatalogDiff(client: CatalogApi, target: string | null, base: 'approved' | string): CatalogDiffState {
  const [state, setState] = useState<{ loading: boolean; error: Error | null; against: string | null; base: CatalogFile[]; target: CatalogFile[] }>({
    loading: target !== null,
    error: null,
    against: null,
    base: [],
    target: [],
  });
  const clientRef = useRef(client);
  clientRef.current = client;

  useEffect(() => {
    if (target === null) return undefined;
    let cancelled = false;
    setState({ loading: true, error: null, against: null, base: [], target: [] });
    (async () => {
      try {
        let detail: CatalogVersionDetail;
        let against: string | null;
        let baseFiles: CatalogFile[] = [];
        if (base === 'approved') {
          detail = await clientRef.current.get(target, 'approved');
          against = detail.diff?.against ?? null;
          if (against !== null) baseFiles = against === target ? detail.files : (await clientRef.current.get(against)).files;
        } else {
          [detail, baseFiles] = await Promise.all([clientRef.current.get(target), clientRef.current.get(base).then((other) => other.files)]);
          against = base;
        }
        if (!cancelled) setState({ loading: false, error: null, against, base: baseFiles, target: detail.files });
      } catch (caught) {
        if (!cancelled) setState({ loading: false, error: caught instanceof Error ? caught : new Error(String(caught)), against: null, base: [], target: [] });
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [target, base]);

  const files = useMemo(() => diffCatalogFiles(state.base, state.target), [state.base, state.target]);
  const summary = useMemo(() => (files.length ? summarizeDiff(files) : EMPTY_SUMMARY), [files]);
  return { loading: state.loading, error: state.error, against: state.against, files, summary };
}
