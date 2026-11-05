import { DAY_MS, dayStart, nextUtcMidnight, secondsToUtcMidnight, utcDay } from './usage-day';

describe('UTC days', () => {
  it('utcDay is the UTC calendar day, whatever the local zone', () => {
    expect(utcDay(Date.UTC(2026, 10, 4, 23, 59, 59, 999))).toBe('2026-11-04');
    expect(utcDay(Date.UTC(2026, 10, 5, 0, 0, 0, 0))).toBe('2026-11-05');
    expect(utcDay(new Date('2026-12-31T23:59:59Z'))).toBe('2026-12-31');
  });

  it('seconds to 00:00 UTC, rounded up, at least 1', () => {
    expect(secondsToUtcMidnight(Date.UTC(2026, 10, 4, 12))).toBe(43_200);
    expect(secondsToUtcMidnight(Date.UTC(2026, 10, 4, 23, 59, 59, 500))).toBe(1);
    expect(secondsToUtcMidnight(Date.UTC(2026, 10, 5, 0, 0, 0, 0))).toBe(86_400);
    expect(nextUtcMidnight(Date.UTC(2026, 10, 4, 0, 0, 0, 1))).toBe(Date.UTC(2026, 10, 5));
  });

  it('dayStart is midnight UTC of the day', () => {
    expect(dayStart('2026-11-04').getTime()).toBe(Date.UTC(2026, 10, 4));
    expect(dayStart('2026-11-05').getTime() - dayStart('2026-11-04').getTime()).toBe(DAY_MS);
  });
});
