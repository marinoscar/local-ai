// Loads the alerts for the current filters (`GET /api/localai/alerts`) and keeps them fresh: it polls every 5 s only while
// the page is visible, refreshes at once when the tab returns or the filters change, keeps the last answer on a failed
// refresh and ignores an answer that arrives after a newer one or after unmount (docs/specs/cloud-app.md section 3.11).
// `acknowledge` posts `POST /api/localai/alerts/:id/ack` and then refreshes.
import { useCallback, useEffect, useRef, useState } from 'react';

import { api } from '../../api';
import type { AlertDto, AlertFilters, AlertsResponse } from './types';

/** How often a visible page refreshes (spec 3.11). */
export const ALERTS_POLL_MS = 5000;

export type FetchAlerts = (filters: AlertFilters) => Promise<AlertsResponse>;
export type AcknowledgeAlert = (id: string) => Promise<AlertDto>;

export const defaultFetchAlerts: FetchAlerts = (filters) => {
  const query = new URLSearchParams({ state: filters.state });
  if (filters.nodeId !== '') query.set('nodeId', filters.nodeId);
  return api.get<AlertsResponse>(`/localai/alerts?${query.toString()}`);
};

export const defaultAcknowledge: AcknowledgeAlert = (id) => api.post<AlertDto>(`/localai/alerts/${encodeURIComponent(id)}/ack`);

const isVisible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden';

export interface AlertsState {
  data: AlertsResponse | null;
  loading: boolean;
  error: Error | null;
  updatedAt: Date | null;
  refresh: () => void;
  /** Acknowledges one alert. Rejects with the API's error so the page can say why. */
  acknowledge: (id: string) => Promise<void>;
}

export function useAlerts(filters: AlertFilters, fetchAlerts: FetchAlerts = defaultFetchAlerts, acknowledgeAlert: AcknowledgeAlert = defaultAcknowledge): AlertsState {
  const [data, setData] = useState<AlertsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);
  const fetchRef = useRef(fetchAlerts);
  fetchRef.current = fetchAlerts;
  const filtersRef = useRef(filters);
  filtersRef.current = filters;
  const generation = useRef(0);
  const mounted = useRef(true);

  const load = useCallback(async () => {
    const mine = ++generation.current;
    try {
      const next = await fetchRef.current(filtersRef.current);
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
      timer = setInterval(() => void load(), ALERTS_POLL_MS);
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
    // A new filter is a new question: load again at once (the interval keeps using the latest filters through the ref).
  }, [load, filters.state, filters.nodeId]);

  const acknowledge = useCallback(
    async (id: string) => {
      await acknowledgeAlert(id);
      await load();
    },
    [acknowledgeAlert, load],
  );

  return { data, loading, error, updatedAt, refresh: () => void load(), acknowledge };
}
