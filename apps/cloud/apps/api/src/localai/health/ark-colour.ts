// The ark readiness colour of one root, from the snapshot (docs/specs/cloud-app.md section 4.7). PURE and
// table-tested in `ark-colour.spec.ts`; the web app never recomputes it.
//
//   red    t1 != ok, or items_failed > 0, or the node's last sync failed,
//          or (ark drive) last synced more than `driveCriticalDays` ago
//   amber  t2 != ok, or items_missing > 0,
//          or (ark drive) last synced more than `driveStaleDays` ago
//   green  otherwise
//   grey   no snapshot yet
//
// "Older than N days" counts WHOLE days: 30 days and 23 hours is still 30 days, so green; the first amber day is the
// 31st. The same for 90/91.

export type ArkColour = 'green' | 'amber' | 'red' | 'grey';
export type ArkRootKind = 'internal' | 'ark_drive';
export type ArkTier = 'ok' | 'partial' | 'missing';
export type SyncResult = 'ok' | 'partial' | 'failed' | 'skipped';

export const DEFAULT_DRIVE_STALE_DAYS = 30;
export const DEFAULT_DRIVE_CRITICAL_DAYS = 90;

export interface ArkThresholds {
  driveStaleDays: number;
  driveCriticalDays: number;
}

export interface ArkRootInput {
  kind: ArkRootKind;
  tiers: { t1: ArkTier; t2: ArkTier };
  itemsMissing: number;
  itemsFailed: number;
  /** `ark.drive_last_synced_at` of the snapshot (one value per node). Only read for an `ark_drive` root. */
  driveLastSyncedAt: Date | null;
  /** `sync.last_run.result`, or null when the node never synced. */
  lastSyncResult: SyncResult | null;
  now: Date;
}

const DAY_MS = 86_400_000;

/** Whole days since `at` (floor); null when there is no instant. Never negative (a clock a little ahead reads 0). */
export function wholeDaysSince(at: Date | null, now: Date): number | null {
  if (at === null) return null;
  return Math.max(0, Math.floor((now.getTime() - at.getTime()) / DAY_MS));
}

export function arkColour(input: ArkRootInput | null, thresholds: ArkThresholds): ArkColour {
  if (input === null) return 'grey';
  const driveAge = input.kind === 'ark_drive' ? wholeDaysSince(input.driveLastSyncedAt, input.now) : null;
  if (
    input.tiers.t1 !== 'ok' ||
    input.itemsFailed > 0 ||
    input.lastSyncResult === 'failed' ||
    (driveAge !== null && driveAge > thresholds.driveCriticalDays)
  ) {
    return 'red';
  }
  if (input.tiers.t2 !== 'ok' || input.itemsMissing > 0 || (driveAge !== null && driveAge > thresholds.driveStaleDays)) {
    return 'amber';
  }
  return 'green';
}

/**
 * The words next to the chip, in the order the colour rules test them (so the label names the reason for the colour).
 * An unplugged drive reads "Drive not plugged in · last synced 6 days ago" whatever its colour.
 */
export function arkLabel(input: ArkRootInput | null, present: boolean, colour: ArkColour): string {
  if (input === null) return 'no data yet';
  const driveAge = input.kind === 'ark_drive' ? wholeDaysSince(input.driveLastSyncedAt, input.now) : null;
  if (input.kind === 'ark_drive' && !present) {
    return driveAge === null ? 'Drive not plugged in · never synced' : `Drive not plugged in · last synced ${plural(driveAge, 'day')} ago`;
  }
  if (input.tiers.t1 !== 'ok') return `tier 1 ${input.tiers.t1}`;
  if (input.itemsFailed > 0) return `${plural(input.itemsFailed, 'item')} failed`;
  // A plugged-in drive is told by its age (the node's last sync belongs to the internal root and the Sync line).
  if (driveAge !== null) return plural(driveAge, 'day');
  if (input.lastSyncResult === 'failed') return 'sync failed';
  if (input.tiers.t2 !== 'ok') return `tier 2 ${input.tiers.t2}`;
  if (input.itemsMissing > 0) return `${plural(input.itemsMissing, 'item')} missing`;
  if (colour === 'red') return 'needs attention';
  return 'ready';
}

function plural(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? '' : 's'}`;
}
