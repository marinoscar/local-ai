// A polled GET for the Catalog page, in the pattern of `useLabNodes`: it loads at once, then repeats every
// `intervalFor(data)` ms while the tab is visible (Page Visibility API), refreshes when the tab comes back, and never
// lets a slow answer overwrite a newer one or set state after unmount. `null` from `intervalFor` stops the polling.
// Changing `key` resets the data (another version).
import { useCallback, useEffect, useRef, useState } from 'react';

export interface PolledState<T> {
  data: T | null;
  /** True until the first answer (success or failure) for the current key. */
  loading: boolean;
  /** The last failure, kept while the previous data stays on screen; cleared by the next success. */
  error: Error | null;
  refresh: () => void;
}

const isVisible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden';

export function usePolled<T>(
  key: string | null,
  fetcher: () => Promise<T>,
  intervalFor: (data: T | null) => number | null,
  /** A value whose change reloads at once (for example the list's `lintedAt` of the version shown). */
  reloadToken: unknown = null,
): PolledState<T> {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(key !== null);
  const [error, setError] = useState<Error | null>(null);
  const fetchRef = useRef(fetcher);
  fetchRef.current = fetcher;
  const generation = useRef(0);
  const mounted = useRef(true);

  const load = useCallback(async () => {
    if (key === null) return;
    const mine = ++generation.current;
    try {
      const next = await fetchRef.current();
      if (!mounted.current || mine !== generation.current) return;
      setData(next);
      setError(null);
    } catch (caught) {
      if (!mounted.current || mine !== generation.current) return;
      setError(caught instanceof Error ? caught : new Error(String(caught)));
    } finally {
      if (mounted.current && mine === generation.current) setLoading(false);
    }
  }, [key]);

  // Another key is another resource: forget the old one.
  useEffect(() => {
    setData(null);
    setError(null);
    setLoading(key !== null);
  }, [key]);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // Load when the resource (or the caller's token) changes. The polling below never reloads on its own start, so a
  // change of interval (5 s to 2 s when a lint is awaited) costs no extra request.
  useEffect(() => {
    void load();
  }, [load, reloadToken]);

  const interval = intervalFor(data);
  useEffect(() => {
    if (key === null) return undefined;
    let timer: ReturnType<typeof setInterval> | null = null;
    const stop = () => {
      if (timer !== null) clearInterval(timer);
      timer = null;
    };
    const start = () => {
      stop();
      if (interval !== null) timer = setInterval(() => void load(), interval);
    };
    const onVisibility = () => {
      if (isVisible()) {
        void load();
        start();
      } else {
        stop();
      }
    };
    if (isVisible()) start();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      stop();
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [key, interval, load]);

  return { data, loading, error, refresh: () => void load() };
}
