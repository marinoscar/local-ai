// The rate-limit headers of every authenticated response (docs/specs/gateway.md section 3.2):
//   x-ratelimit-limit-requests / -remaining-requests   the key's requests per rolling minute
//   x-ratelimit-limit-tokens / -remaining-tokens        the key's daily token budget (UTC day)
// A key with no daily budget (`null`, service keys only) gets the two request headers and no token headers: there is
// no number that would be true. Pure.

export interface RateLimitSnapshot {
  limitRequests: number;
  remainingRequests: number;
  /** null = unlimited. */
  limitTokens: bigint | null;
  remainingTokens: bigint | null;
}

export const RATE_LIMIT_HEADER_NAMES = [
  'x-ratelimit-limit-requests',
  'x-ratelimit-remaining-requests',
  'x-ratelimit-limit-tokens',
  'x-ratelimit-remaining-tokens',
] as const;

export function buildRateLimitHeaders(snapshot: RateLimitSnapshot): Record<string, string> {
  const headers: Record<string, string> = {
    'x-ratelimit-limit-requests': String(snapshot.limitRequests),
    'x-ratelimit-remaining-requests': String(Math.max(0, snapshot.remainingRequests)),
  };
  if (snapshot.limitTokens !== null) {
    const remaining = snapshot.remainingTokens ?? snapshot.limitTokens;
    headers['x-ratelimit-limit-tokens'] = snapshot.limitTokens.toString();
    headers['x-ratelimit-remaining-tokens'] = (remaining < 0n ? 0n : remaining).toString();
  }
  return headers;
}
