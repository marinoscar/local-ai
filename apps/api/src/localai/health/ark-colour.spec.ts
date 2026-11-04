// The ark readiness colour table of docs/specs/cloud-app.md section 4.7 (tiers x drive age x last sync), the labels,
// and the unplugged-drive wording.
import { arkColour, arkLabel, wholeDaysSince, type ArkRootInput, type ArkThresholds } from './ark-colour';

const NOW = new Date('2026-11-03T12:00:00.000Z');
const daysAgo = (days: number, extraHours = 0) => new Date(NOW.getTime() - days * 86_400_000 - extraHours * 3_600_000);
const THRESHOLDS: ArkThresholds = { driveStaleDays: 30, driveCriticalDays: 90 };

const internal = (patch: Partial<ArkRootInput> = {}): ArkRootInput => ({
  kind: 'internal',
  tiers: { t1: 'ok', t2: 'ok' },
  itemsMissing: 0,
  itemsFailed: 0,
  driveLastSyncedAt: null,
  lastSyncResult: 'ok',
  now: NOW,
  ...patch,
});
const drive = (patch: Partial<ArkRootInput> = {}): ArkRootInput => internal({ kind: 'ark_drive', driveLastSyncedAt: daysAgo(6), ...patch });

describe('arkColour: internal root', () => {
  it.each<[string, Partial<ArkRootInput>, string]>([
    ['t1 ok, t2 ok, nothing wrong', {}, 'green'],
    ['t1 partial', { tiers: { t1: 'partial', t2: 'ok' } }, 'red'],
    ['t1 missing', { tiers: { t1: 'missing', t2: 'ok' } }, 'red'],
    ['t1 partial beats t2 partial', { tiers: { t1: 'partial', t2: 'partial' } }, 'red'],
    ['t2 partial', { tiers: { t1: 'ok', t2: 'partial' } }, 'amber'],
    ['t2 missing', { tiers: { t1: 'ok', t2: 'missing' } }, 'amber'],
    ['one item missing', { itemsMissing: 1 }, 'amber'],
    ['one item failed', { itemsFailed: 1 }, 'red'],
    ['items failed beats items missing', { itemsFailed: 1, itemsMissing: 4 }, 'red'],
    ['last sync failed', { lastSyncResult: 'failed' }, 'red'],
    ['last sync partial is not a failure', { lastSyncResult: 'partial' }, 'green'],
    ['last sync skipped is not a failure', { lastSyncResult: 'skipped' }, 'green'],
    ['never synced (null) is not a failure', { lastSyncResult: null }, 'green'],
    ['last sync failed beats t2 partial', { lastSyncResult: 'failed', tiers: { t1: 'ok', t2: 'partial' } }, 'red'],
  ])('%s -> %s', (_name, patch, expected) => {
    expect(arkColour(internal(patch), THRESHOLDS)).toBe(expected);
  });

  it('an internal root ignores drive_last_synced_at, however old', () => {
    expect(arkColour(internal({ driveLastSyncedAt: daysAgo(400) }), THRESHOLDS)).toBe('green');
  });

  it('no snapshot yet -> grey', () => {
    expect(arkColour(null, THRESHOLDS)).toBe('grey');
  });
});

describe('arkColour: ark drive age (whole days, "older than" is strict)', () => {
  it.each<[string, Date | null, string]>([
    ['0 days', daysAgo(0), 'green'],
    ['6 days', daysAgo(6), 'green'],
    ['30 days exactly', daysAgo(30), 'green'],
    ['30 days 23 hours (still 30 whole days)', daysAgo(30, 23), 'green'],
    ['31 days', daysAgo(31), 'amber'],
    ['41 days', daysAgo(41), 'amber'],
    ['90 days exactly', daysAgo(90), 'amber'],
    ['90 days 23 hours (still 90 whole days)', daysAgo(90, 23), 'amber'],
    ['91 days', daysAgo(91), 'red'],
    ['400 days', daysAgo(400), 'red'],
    ['a date in the future (clock skew)', new Date(NOW.getTime() + 3_600_000), 'green'],
    ['never synced to a drive (null): no age rule', null, 'green'],
  ])('%s -> %s', (_name, at, expected) => {
    expect(arkColour(drive({ driveLastSyncedAt: at }), THRESHOLDS)).toBe(expected);
  });

  it('a fresh drive with t2 partial is amber, with t1 partial is red', () => {
    expect(arkColour(drive({ tiers: { t1: 'ok', t2: 'partial' } }), THRESHOLDS)).toBe('amber');
    expect(arkColour(drive({ tiers: { t1: 'partial', t2: 'ok' } }), THRESHOLDS)).toBe('red');
  });

  it('honours custom thresholds from settings', () => {
    const custom: ArkThresholds = { driveStaleDays: 7, driveCriticalDays: 14 };
    expect(arkColour(drive({ driveLastSyncedAt: daysAgo(7) }), custom)).toBe('green');
    expect(arkColour(drive({ driveLastSyncedAt: daysAgo(8) }), custom)).toBe('amber');
    expect(arkColour(drive({ driveLastSyncedAt: daysAgo(14) }), custom)).toBe('amber');
    expect(arkColour(drive({ driveLastSyncedAt: daysAgo(15) }), custom)).toBe('red');
  });
});

describe('wholeDaysSince', () => {
  it('floors, never goes negative, and is null without an instant', () => {
    expect(wholeDaysSince(daysAgo(6, 20), NOW)).toBe(6);
    expect(wholeDaysSince(new Date(NOW.getTime() + 86_400_000), NOW)).toBe(0);
    expect(wholeDaysSince(null, NOW)).toBeNull();
  });
});

describe('arkLabel', () => {
  const label = (input: ArkRootInput | null, present = true) => arkLabel(input, present, arkColour(input, THRESHOLDS));

  it('names the reason for the colour', () => {
    expect(label(internal())).toBe('ready');
    expect(label(internal({ tiers: { t1: 'partial', t2: 'ok' } }))).toBe('tier 1 partial');
    expect(label(internal({ tiers: { t1: 'ok', t2: 'partial' } }))).toBe('tier 2 partial');
    expect(label(internal({ itemsFailed: 1 }))).toBe('1 item failed');
    expect(label(internal({ itemsFailed: 3 }))).toBe('3 items failed');
    expect(label(internal({ itemsMissing: 2 }))).toBe('2 items missing');
    expect(label(internal({ lastSyncResult: 'failed' }))).toBe('sync failed');
    expect(label(null)).toBe('no data yet');
  });

  it('a drive row keeps its age when the node last sync failed; a drive with no known age falls back to the other reasons', () => {
    expect(label(drive({ driveLastSyncedAt: daysAgo(41), lastSyncResult: 'failed' }))).toBe('41 days');
    expect(label(drive({ driveLastSyncedAt: null, lastSyncResult: 'failed' }))).toBe('sync failed');
    expect(label(drive({ driveLastSyncedAt: null, tiers: { t1: 'ok', t2: 'partial' } }))).toBe('tier 2 partial');
    expect(label(drive({ driveLastSyncedAt: null }))).toBe('ready');
  });

  it('shows a plugged-in drive by its age', () => {
    expect(label(drive({ driveLastSyncedAt: daysAgo(6) }))).toBe('6 days');
    expect(label(drive({ driveLastSyncedAt: daysAgo(1) }))).toBe('1 day');
    expect(label(drive({ driveLastSyncedAt: daysAgo(41) }))).toBe('41 days');
    expect(label(drive({ driveLastSyncedAt: daysAgo(120) }))).toBe('120 days');
  });

  it('labels an unplugged drive "Drive not plugged in · last synced N days ago"', () => {
    expect(label(drive({ driveLastSyncedAt: daysAgo(6) }), false)).toBe('Drive not plugged in · last synced 6 days ago');
    expect(label(drive({ driveLastSyncedAt: daysAgo(1) }), false)).toBe('Drive not plugged in · last synced 1 day ago');
    expect(label(drive({ driveLastSyncedAt: null }), false)).toBe('Drive not plugged in · never synced');
  });
});
