import { parseLocalaiSettings } from './lab-settings.service';

describe('parseLocalaiSettings', () => {
  it('defaults: 30 and 90 days, no latest agent version', () => {
    for (const stored of [undefined, null, {}, 'junk', 42, { alerts: {}, agent: {} }]) {
      expect(parseLocalaiSettings(stored)).toEqual({ thresholds: { driveStaleDays: 30, driveCriticalDays: 90 }, agentLatestVersion: null });
    }
  });

  it('reads the stored thresholds and the latest agent version', () => {
    expect(parseLocalaiSettings({ alerts: { driveStaleDays: 14, driveCriticalDays: 60 }, agent: { latestVersion: '0.4.2' } })).toEqual({
      thresholds: { driveStaleDays: 14, driveCriticalDays: 60 },
      agentLatestVersion: '0.4.2',
    });
  });

  it('keeps the good fields when a neighbour is damaged', () => {
    expect(parseLocalaiSettings({ alerts: { driveStaleDays: 'soon', driveCriticalDays: 45 }, agent: { latestVersion: 7 } })).toEqual({
      thresholds: { driveStaleDays: 30, driveCriticalDays: 45 },
      agentLatestVersion: null,
    });
    expect(parseLocalaiSettings({ alerts: { driveStaleDays: -3, driveCriticalDays: 0 } }).thresholds).toEqual({ driveStaleDays: 30, driveCriticalDays: 90 });
  });

  it('never lets the critical threshold fall below the stale one', () => {
    expect(parseLocalaiSettings({ alerts: { driveStaleDays: 60, driveCriticalDays: 20 } }).thresholds).toEqual({ driveStaleDays: 60, driveCriticalDays: 60 });
  });
});
