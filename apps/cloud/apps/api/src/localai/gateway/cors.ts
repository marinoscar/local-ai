// CORS of the gateway (docs/specs/gateway.md section 6). None by default: with `gateway.corsOrigins` empty the gateway
// never sends an `Access-Control-*` header. With origins listed, exactly those (exact string match) are answered, with
// the fixed method/header lists and NEVER `Access-Control-Allow-Credentials` (keys are bearer tokens, cookies unused).
import { z } from 'zod';

/** The settings key (`localai` namespace) holding the allowed browser origins. */
export const GATEWAY_CORS_ORIGINS_KEY = 'gateway.corsOrigins';

/** One origin: scheme, host and optional port, no path, no wildcard. `*` (and `https://*.example`) is refused. */
const ORIGIN = /^https?:\/\/[A-Za-z0-9.-]+(:\d{1,5})?$/;

/** The schema of `gateway.corsOrigins`: a list of exact origins (default `[]`); `*` is a validation error. */
export const GatewayCorsOriginsSchema = z
  .array(z.string().max(255).regex(ORIGIN, 'An origin looks like https://app.example.test (no path, no wildcard)'))
  .max(50)
  .default([]);

export type CorsOrigins = readonly string[];

/** Pure: a stored value (anything) to the origin list. An invalid value is "no origins" (CORS stays off). */
export function parseCorsOrigins(stored: unknown): string[] {
  const parsed = GatewayCorsOriginsSchema.safeParse(stored);
  return parsed.success ? parsed.data : [];
}

const PREFLIGHT_HEADERS: Readonly<Record<string, string>> = {
  'access-control-allow-methods': 'GET, POST, OPTIONS',
  'access-control-allow-headers': 'authorization, content-type',
  'access-control-max-age': '600',
};

/**
 * The CORS headers for a request whose `Origin` header is `origin`: none when the list is empty, the request has no
 * origin or the origin is not listed. `preflight` adds the method, header and max-age lines.
 */
export function corsHeaders(origin: string | string[] | undefined, allowed: CorsOrigins, preflight: boolean): Record<string, string> {
  if (allowed.length === 0) return {};
  const value = Array.isArray(origin) ? origin[0] : origin;
  if (typeof value !== 'string' || !allowed.includes(value)) return {};
  return { 'access-control-allow-origin': value, vary: 'Origin', ...(preflight ? PREFLIGHT_HEADERS : {}) };
}
