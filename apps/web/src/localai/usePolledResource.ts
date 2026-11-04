// Loads a resource and keeps it fresh: it polls every `intervalMs` while the page is visible, stops while the tab is
// hidden (Page Visibility API) and refreshes at once when the tab comes back (docs/specs/cloud-app.md section 3.11,
// no SSE in v1). A slow answer never overwrites a newer one and nothing sets state after unmount. The last good data
// stays on screen when a refresh fails; `error` holds the failure until the next success.
import { useCallback, useEffect, useRef, useState } from 'react';

export interface PolledResource<T> {
  data: T | null;
  /** True until the first answer (success or failure). */
  loading: boolean;
  error: Error | null;
  /** When the data on screen arrived. */
  updatedAt: Date | null;
  refresh: () => void;
}

const isVisible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden';

export function usePolledResource<T>(fetcher: () => Promise<T>, intervalMs: number): PolledResource<T> {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);
  const fetchRef = useRef(fetcher);
  fetchRef.current = fetcher;
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
      timer = setInterval(() => void load(), intervalMs);
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
  }, [load, intervalMs]);

  return { data, loading, error, updatedAt, refresh: () => void load() };
}
