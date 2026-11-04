import { DEFAULT_ALERT_SETTINGS, parseAlertSettings } from './alert-settings';

describe('parseAlertSettings', () => {
  it('has the documented defaults (spec 3.10) when nothing is stored', () => {
    expect(parseAlertSettings(undefined)).toEqual({
      driveStaleDays: 30,
      driveCriticalDays: 90,
      nodeStaleMinutes: 10,
      nodeOfflineHours: 24,
      catalogBehindDays: 14,
      repeatHours: 24,
      agentLatestVersion: null,
    });
    expect(parseAlertSettings(null)).toEqual(DEFAULT_ALERT_SETTINGS);
  });

  it('reads the nested form and the dotted keys', () => {
    expect(parseAlertSettings({ alerts: { driveStaleDays: 7, repeatHours: 6 }, agent: { latestVersion: '0.4.2' } })).toMatchObject({ driveStaleDays: 7, repeatHours: 6, agentLatestVersion: '0.4.2' });
    expect(parseAlertSettings({ 'alerts.nodeStaleMinutes': 20, 'alerts.nodeOfflineHours': 48, 'alerts.catalogBehindDays': 3 })).toMatchObject({ nodeStaleMinutes: 20, nodeOfflineHours: 48, catalogBehindDays: 3 });
  });

  it('falls back field by field on a value that is not a positive integer', () => {
    expect(parseAlertSettings({ alerts: { driveStaleDays: -1, driveCriticalDays: '90', nodeStaleMinutes: 0, repeatHours: 1.5, nodeOfflineHours: 99999 } })).toMatchObject({
      driveStaleDays: 30,
      driveCriticalDays: 90,
      nodeStaleMinutes: 10,
      repeatHours: 24,
      nodeOfflineHours: 24,
    });
  });

  it('never lets the critical drive threshold fall below the stale one', () => {
    expect(parseAlertSettings({ alerts: { driveStaleDays: 60, driveCriticalDays: 10 } })).toMatchObject({ driveStaleDays: 60, driveCriticalDays: 60 });
  });

  it('ignores a non-string or empty agent version', () => {
    expect(parseAlertSettings({ agent: { latestVersion: 4 } }).agentLatestVersion).toBeNull();
    expect(parseAlertSettings({ agent: { latestVersion: '' } }).agentLatestVersion).toBeNull();
  });
});
