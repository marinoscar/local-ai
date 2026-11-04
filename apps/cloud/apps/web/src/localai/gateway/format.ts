// How a key's limits, models and expiry read in the table (the wireframe of gateway.md section 3.8). Pure.
import type { GatewayKey } from './types';

/** 2 000 000 -> "2M", 1 500 -> "1.5k", 800 -> "800". */
export function compactCount(value: number): string {
  if (value >= 1_000_000) return `${trim(value / 1_000_000)}M`;
  if (value >= 1_000) return `${trim(value / 1_000)}k`;
  return String(value);
}

const trim = (n: number) => String(Math.round(n * 10) / 10);

/** "60 rpm · 4 · 2M/d": requests per minute, concurrent requests, tokens per day (∞ when unlimited). */
export function describeLimits(limits: GatewayKey['limits']): string {
  return `${limits.rpm} rpm · ${limits.concurrent} · ${limits.dailyTokens === null ? '∞' : `${compactCount(limits.dailyTokens)}/d`}`;
}

/** "all" for an empty list, else the names (the table cell shows the first and a count). */
export function describeModels(models: readonly string[]): string {
  if (models.length === 0) return 'all';
  return models.length === 1 ? models[0]! : `${models[0]} +${models.length - 1}`;
}

/** The calendar day (UTC) of an expiry, or "never". */
export function describeExpiry(iso: string | null): string {
  return iso === null ? 'never' : iso.slice(0, 10);
}
