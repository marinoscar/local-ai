// UTC days, the unit of the daily token budget and of the rollup table (docs/specs/gateway.md sections 4.1 and 4.5).
// Pure helpers; one place so the limiter, the recorder and the usage API cannot disagree about where a day ends.

export const DAY_MS = 86_400_000;

/** `2026-11-04`: the UTC calendar day of an instant (epoch ms or Date). */
export function utcDay(at: number | Date): string {
  return new Date(typeof at === 'number' ? at : at.getTime()).toISOString().slice(0, 10);
}

/** Epoch ms of 00:00 UTC of the day after the one `at` falls in. */
export function nextUtcMidnight(at: number): number {
  return Math.floor(at / DAY_MS) * DAY_MS + DAY_MS;
}

/** Whole seconds from `at` to the next 00:00 UTC, at least 1 (the `Retry-After` of a spent daily budget). */
export function secondsToUtcMidnight(at: number): number {
  return Math.max(1, Math.ceil((nextUtcMidnight(at) - at) / 1000));
}

/** `2026-11-04` as a Date at 00:00 UTC (the value of a `@db.Date` column). */
export function dayStart(day: string): Date {
  return new Date(`${day}T00:00:00.000Z`);
}
