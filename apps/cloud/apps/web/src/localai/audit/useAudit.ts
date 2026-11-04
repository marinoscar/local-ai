// Loads the audit trail (`GET /api/localai/audit`) for the current filters. Page 1 of the trail refreshes every 5 s
// while the tab is visible (docs/specs/cloud-app.md section 3.11); a later page is left alone, because new events
// would shift its rows under the reader's eyes. The filter options load once.
import { useCallback, useEffect, useRef, useState } from 'react';

import { api } from '../../api';
import { auditQueryString } from './format';
import type { AuditFilterOptions, AuditFilterState, AuditPageData } from './types';

export const AUDIT_POLL_MS = 5000;

export type FetchAudit = (query: string) => Promise<AuditPageData>;
export type FetchAuditFilters = () => Promise<AuditFilterOptions>;

const defaultFetchAudit: FetchAudit = (query) => api.get<AuditPageData>(`/localai/audit${query ? `?${query}` : ''}`);
const defaultFetchFilters: FetchAuditFilters = () => api.get<AuditFilterOptions>('/localai/audit/filters');

const isVisible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden';
const asError = (caught: unknown) => (caught instanceof Error ? caught : new Error(String(caught)));

export interface AuditState {
  data: AuditPageData | null;
  loading: boolean;
  error: Error | null;
  refresh: () => void;
}

export function useAudit(filters: AuditFilterState, fetchAudit: FetchAudit = defaultFetchAudit, enabled = true): AuditState {
  const [data, setData] = useState<AuditPageData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const fetchRef = useRef(fetchAudit);
  fetchRef.current = fetchAudit;
  const generation = useRef(0);
  const mounted = useRef(true);
  const query = auditQueryString(filters);
  const live = filters.page === 1;

  const load = useCallback(async () => {
    const mine = ++generation.current;
    try {
      const next = await fetchRef.current(query);
      if (!mounted.current || mine !== generation.current) return;
      setData(next);
      setError(null);
    } catch (caught) {
      if (!mounted.current || mine !== generation.current) return;
      setError(asError(caught));
    } finally {
      if (mounted.current && mine === generation.current) setLoading(false);
    }
  }, [query]);

  useEffect(() => {
    mounted.current = true;
    if (!enabled) return undefined;
    setLoading(true);
    let timer: ReturnType<typeof setInterval> | null = null;
    const stop = () => {
      if (timer !== null) clearInterval(timer);
      timer = null;
    };
    const start = () => {
      stop();
      if (live) timer = setInterval(() => void load(), AUDIT_POLL_MS);
    };
    const onVisibility = () => {
      if (isVisible()) {
        void load();
        start();
      } else {
        stop();
      }
    };
    void load();
    if (isVisible()) start();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      mounted.current = false;
      stop();
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [load, live, enabled]);

  return { data, loading, error, refresh: () => void load() };
}

export function useAuditFilterOptions(fetchFilters: FetchAuditFilters = defaultFetchFilters): { options: AuditFilterOptions | null; error: Error | null } {
  const [options, setOptions] = useState<AuditFilterOptions | null>(null);
  const [error, setError] = useState<Error | null>(null);
  const fetchRef = useRef(fetchFilters);
  fetchRef.current = fetchFilters;
  useEffect(() => {
    let alive = true;
    fetchRef.current().then(
      (next) => {
        if (alive) setOptions(next);
      },
      (caught) => {
        if (alive) setError(asError(caught));
      },
    );
    return () => {
      alive = false;
    };
  }, []);
  return { options, error };
}
