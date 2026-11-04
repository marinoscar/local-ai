import { usageRange, usageUrl } from './usageApi';

const NOW = new Date('2026-11-04T23:30:00.000-05:00'); // 04:30 UTC on the 5th: the range counts UTC days

describe('the usage request', () => {
  it('asks for whole UTC days ending today', () => {
    expect(usageRange(30, NOW)).toEqual({ from: '2026-10-07', to: '2026-11-05' });
    expect(usageRange(7, new Date('2026-11-04T12:00:00Z'))).toEqual({ from: '2026-10-29', to: '2026-11-04' });
  });

  it('builds the query, with the key and the admin scope only when chosen', () => {
    expect(usageUrl({ days: 7, groupBy: 'model', keyId: null, all: false }, new Date('2026-11-04T12:00:00Z'))).toBe('/localai/gateway/usage?from=2026-10-29&to=2026-11-04&groupBy=model');
    expect(usageUrl({ days: 30, groupBy: 'day', keyId: 'k-1', all: true }, new Date('2026-11-04T12:00:00Z'))).toBe('/localai/gateway/usage?from=2026-10-06&to=2026-11-04&groupBy=day&keyId=k-1&all=1');
  });
});
