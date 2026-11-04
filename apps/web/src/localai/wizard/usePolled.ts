// Polls an async read at a fixed interval while the tab is visible (Page Visibility API), refreshes the moment the tab
// returns, keeps the last answer when a refresh fails, and ignores an answer that arrives after a newer one or after
// unmount. The same behaviour as the Lab hook (`../lab/useLabNodes.ts`), for the wizard's two polls.
import { useCallback, useEffect, useRef, useState } from 'react';

export interface PolledState<T> {
  data: T | null;
  loading: boolean;
  error: Error | null;
  /** Reads now. */
  refresh: () => void;
  /** Replaces the data with an answer the caller already has (the response of an action). */
  set: (value: T) => void;
}

const isVisible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden';

/**
 * @param read      the read; called with no arguments, may reject
 * @param intervalMs poll period
 * @param enabled   false stops polling and reading (e.g. once both flags are green)
 */
export function usePolled<T>(read: () => Promise<T>, intervalMs: number, enabled = true): PolledState<T> {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const readRef = useRef(read);
  readRef.current = read;
  const generation = useRef(0);
  const mounted = useRef(true);

  const load = useCallback(async () => {
    const mine = ++generation.current;
    try {
      const next = await readRef.current();
      if (!mounted.current || mine !== generation.current) return;
      setData(next);
      setError(null);
    } catch (caught) {
      if (!mounted.current || mine !== generation.current) return;
      setError(caught instanceof Error ? caught : new Error(String(caught)));
    } finally {
      if (mounted.current && mine === generation.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    if (!enabled) return () => undefined;
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
  }, [load, intervalMs, enabled]);

  const set = useCallback((value: T) => {
    generation.current += 1; // an older read still in flight must not overwrite it
    setData(value);
    setError(null);
    setLoading(false);
  }, []);

  return { data, loading, error, refresh: () => void load(), set };
}
