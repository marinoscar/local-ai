// The one usage route. The range is whole UTC days ending today, because the daily table and the budget are UTC days.
import { api } from '../../api';
import type { UsageFilters, UsageResponse } from './usageTypes';

const DAY_MS = 86_400_000;
const isoDay = (ms: number): string => new Date(ms).toISOString().slice(0, 10);

/** `from` and `to` (UTC days, inclusive) of "the last N days" at `now`. */
export function usageRange(days: number, now: Date): { from: string; to: string } {
  const today = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
  return { from: isoDay(today - (days - 1) * DAY_MS), to: isoDay(today) };
}

export function usageUrl(filters: UsageFilters, now: Date): string {
  const { from, to } = usageRange(filters.days, now);
  const query = new URLSearchParams({ from, to, groupBy: filters.groupBy });
  if (filters.keyId) query.set('keyId', filters.keyId);
  if (filters.all) query.set('all', '1');
  return `/localai/gateway/usage?${query.toString()}`;
}

export const fetchUsage = (filters: UsageFilters, now: Date = new Date()): Promise<UsageResponse> => api.get<UsageResponse>(usageUrl(filters, now));
