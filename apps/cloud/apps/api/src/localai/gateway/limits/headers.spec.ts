import { buildRateLimitHeaders, RATE_LIMIT_HEADER_NAMES } from './headers';

describe('buildRateLimitHeaders', () => {
  it('carries the four headers of the spec for a key with a daily budget', () => {
    const headers = buildRateLimitHeaders({ limitRequests: 60, remainingRequests: 59, limitTokens: 2_000_000n, remainingTokens: 1_999_000n });
    expect(headers).toEqual({
      'x-ratelimit-limit-requests': '60',
      'x-ratelimit-remaining-requests': '59',
      'x-ratelimit-limit-tokens': '2000000',
      'x-ratelimit-remaining-tokens': '1999000',
    });
    expect(Object.keys(headers).sort()).toEqual([...RATE_LIMIT_HEADER_NAMES].sort());
  });

  it('never goes below zero (an overshoot is not a negative number)', () => {
    const headers = buildRateLimitHeaders({ limitRequests: 60, remainingRequests: -3, limitTokens: 100n, remainingTokens: -50n });
    expect(headers['x-ratelimit-remaining-requests']).toBe('0');
    expect(headers['x-ratelimit-remaining-tokens']).toBe('0');
  });

  it('leaves the token headers out for a key with no daily budget', () => {
    const headers = buildRateLimitHeaders({ limitRequests: 600, remainingRequests: 600, limitTokens: null, remainingTokens: null });
    expect(Object.keys(headers).sort()).toEqual(['x-ratelimit-limit-requests', 'x-ratelimit-remaining-requests']);
  });
});
