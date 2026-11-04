// Loads the Lab overview (`GET /api/localai/nodes`) and keeps it fresh: it polls every 5 s while the page is visible and
// stops while the tab is hidden (Page Visibility API), then refreshes at once when the tab comes back. No SSE in v1
// (docs/specs/cloud-app.md section 3.11). Everything on the page comes from this response; nothing is derived here.
import { useCallback, useEffect, useRef, useState } from 'react';
import type { LabNodesResponse } from '@localai/ui';

import { api } from '../../api';

/** How often a visible page refreshes (spec 3.11). */
export const LAB_POLL_MS = 5000;

export interface LabNodesState {
  data: LabNodesResponse | null;
  /** True until the first answer (success or failure). */
  loading: boolean;
  /** The last failure, kept while the previous data stays on screen; cleared by the next success. */
  error: Error | null;
  /** When the data on screen arrived. */
  updatedAt: Date | null;
  refresh: () => void;
}

/** The transport, replaceable in tests. */
export type FetchLabNodes = () => Promise<LabNodesResponse>;

const defaultFetch: FetchLabNodes = () => api.get<LabNodesResponse>('/localai/nodes');

const isVisible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden';

export function useLabNodes(fetchNodes: FetchLabNodes = defaultFetch): LabNodesState {
  const [data, setData] = useState<LabNodesResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);
  const fetchRef = useRef(fetchNodes);
  fetchRef.current = fetchNodes;
  // A slow answer must not overwrite a newer one, and nothing may set state after unmount.
  const generation = useRef(0);
  const mounted = useRef(true);

  const load = useCallback(async () => {
    const mine = ++generation.current;
    try {
      const next = await fetchRef.current();
      if (!mounted.current || mine !== generation.current) return;
      setData(next);
      setError(null);
      setUpdatedAt(new Date());
    } catch (caught) {
      if (!mounted.current || mine !== generation.current) return;
      setError(caught instanceof Error ? caught : new Error(String(caught)));
    } finally {
      if (mounted.current && mine === generation.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    let timer: ReturnType<typeof setInterval> | null = null;
    const stop = () => {
      if (timer !== null) clearInterval(timer);
      timer = null;
    };
    const start = () => {
      stop();
      timer = setInterval(() => void load(), LAB_POLL_MS);
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
  }, [load]);

  return { data, loading, error, updatedAt, refresh: () => void load() };
}
