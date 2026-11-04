// The alert rules (docs/specs/cloud-app.md section 4.9). PURE: no database, no clock (the caller passes `now`), so every
// row of the kind table is tested with fixed instants, boundaries included, in `alert-rules.spec.ts`.
//
// A rule answers one question per kind: "is this alert true for this node right now?". `evaluateNodeAlerts` returns the
// DESIRED active set of one node; `AlertsService` compares it with the active rows and raises, refreshes, escalates or
// clears. Rules read the node's stored columns and its last snapshot (`lastState`, validated at ingestion, read
// defensively here: an older snapshot lacking a field must never throw).
//
// HYSTERESIS. Raising and clearing are not mirror images for the two silence kinds: `node.stale` is raised at
// `alerts.nodeStaleMinutes` of silence but only cleared when the node is healthy again (a snapshot at most 2 minutes
// old), so a node that comes back for one snapshot and goes quiet again does not flap. `node.offline` is held until the
// heartbeat resumes, and `sync.failed` until the next run is ok or partial (a `skipped` run, for example in Offline
// mode, replaces `last_run` without proving anything). `active` carries the node's currently active alerts so a rule can
// hold one.
//
// EXPECTED SILENCE (spec 4.5) suppresses and clears `node.stale` and `node.offline`: a node in Offline or Survival mode,
// or not enrolled yet, is quiet on purpose. A node that is `pending` raises nothing at all.
import type { NodeHealth } from '@marinoscar/platform-api/nodes';

import { wholeDaysSince } from '../health/ark-colour';
import { ageSeconds, deriveHealth, type LabMode, type LabPhase } from '../health/derive-health';
import { isAgentBehind } from '../health/versions';
import { DOCTOR_CODE_PATTERN, KIND_CODES, NODE_REPORTED_PREFIX, type AlertSeverity } from './alert-kinds';
import type { AlertSettings } from './alert-settings';

/** `key.missing` is raised when the node enrolled at least this long ago (spec 4.9: 10 minutes). */
export const KEY_MISSING_GRACE_MINUTES = 10;

/** What the rules read about one node. A structural subset of the stored row plus what is derived from other tables. */
export interface RuleNode {
  /** The lab node id, `ai-n<N>`. */
  nodeId: string;
  phase: LabPhase;
  mode: LabMode | null;
  lastStateAt: Date | null;
  lastState: unknown;
  agentVersion: string | null;
  catalogVersion: string | null;
  catalogPin: string | null;
  keyReportedAt: Date | null;
  /** EAB's `deriveNodeHealth` of the linked worker; `offline` when the node has no linked worker row. */
  eab: NodeHealth;
  /** The latest sign of life: the worker's last heartbeat or the last snapshot, whichever is later. Null when neither exists. */
  lastContactAt: Date | null;
  /** When the node enrolled (its worker row was registered), for `key.missing`; null when unknown. */
  enrolledAt: Date | null;
}

export interface RuleContext {
  now: Date;
  settings: AlertSettings;
  /** The approved catalog version and when it was approved, or null when none is approved. */
  approvedCatalog: { version: string; approvedAt: Date | null } | null;
}

/** One alert that is true now. `kind` + node is the identity (one active row per pair). */
export interface DesiredAlert {
  kind: string;
  code: string;
  severity: AlertSeverity;
  title: string;
  details: Record<string, unknown>;
}

/** The fields of an active alert a rule needs to hold it. */
export interface ActiveAlertView {
  kind: string;
  code: string;
  severity: AlertSeverity;
  title: string;
  details: Record<string, unknown> | null;
}

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const asString = (value: unknown): string | null => (typeof value === 'string' ? value : null);
const asDate = (value: unknown): Date | null => {
  if (typeof value !== 'string') return null;
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
};

const MINUTE_S = 60;
const HOUR_S = 3600;
const DAY_S = 86_400;

/** "42 min", "3 h", "2 days": a duration for a title, rounded down, never "0". */
export function describeDuration(seconds: number): string {
  if (!Number.isFinite(seconds)) return 'a long time';
  if (seconds >= 2 * DAY_S) return `${Math.floor(seconds / DAY_S)} days`;
  if (seconds >= HOUR_S) return `${Math.floor(seconds / HOUR_S)} h`;
  return `${Math.max(1, Math.floor(seconds / MINUTE_S))} min`;
}

function plural(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? '' : 's'}`;
}

function arkOf(state: unknown): { roots: Record<string, unknown>[]; driveLastSyncedAt: Date | null } | null {
  const ark = isRecord(state) && isRecord(state.ark) ? state.ark : null;
  if (ark === null) return null;
  return { roots: (Array.isArray(ark.roots) ? ark.roots : []).filter(isRecord), driveLastSyncedAt: asDate(ark.drive_last_synced_at) };
}

const isDriveRoot = (root: Record<string, unknown>): boolean => typeof root.id === 'string' && root.id !== 'internal';

/** Hold an active alert as it is (title and severity kept), for a kind the rule cannot re-prove this run. */
function held(active: ReadonlyMap<string, ActiveAlertView>, kind: string): DesiredAlert[] {
  const existing = active.get(kind);
  if (!existing) return [];
  return [{ kind, code: existing.code, severity: existing.severity, title: existing.title, details: existing.details ?? {} }];
}

function nodeStale(node: RuleNode, health: string, ctx: RuleContext, active: ReadonlyMap<string, ActiveAlertView>): DesiredAlert[] {
  // Quiet (expected silence), healthy, and a node that never reported are all "no alert"; mode must have been online.
  if (health === 'quiet' || health === 'healthy' || node.mode !== 'online' || node.lastStateAt === null) return [];
  const age = ageSeconds(node.lastStateAt, ctx.now);
  const raiseAfter = ctx.settings.nodeStaleMinutes * MINUTE_S;
  if (age < raiseAfter && !active.has('node.stale')) return [];
  return [
    {
      kind: 'node.stale',
      code: KIND_CODES['node.stale'],
      severity: 'warning',
      title: `Not seen for ${describeDuration(age)}`,
      details: { lastStateAt: node.lastStateAt.toISOString(), silentSeconds: Math.floor(age) },
    },
  ];
}

function nodeOffline(node: RuleNode, health: string, ctx: RuleContext, active: ReadonlyMap<string, ActiveAlertView>): DesiredAlert[] {
  if (health === 'quiet' || node.mode !== 'online' || node.eab !== 'offline' || node.lastContactAt === null) return [];
  const silent = ageSeconds(node.lastContactAt, ctx.now);
  if (silent < ctx.settings.nodeOfflineHours * HOUR_S && !active.has('node.offline')) return [];
  return [
    {
      kind: 'node.offline',
      code: KIND_CODES['node.offline'],
      severity: 'critical',
      title: `Offline for ${describeDuration(silent)}`,
      details: { lastContactAt: node.lastContactAt.toISOString(), silentSeconds: Math.floor(silent) },
    },
  ];
}

function driveStale(node: RuleNode, ctx: RuleContext): DesiredAlert[] {
  const ark = arkOf(node.lastState);
  if (ark === null) return [];
  const { driveStaleDays, driveCriticalDays } = ctx.settings;
  const days = wholeDaysSince(ark.driveLastSyncedAt, ctx.now);
  if (days === null) {
    // Never synced to a drive: only worth an alert on a node that has reported a drive root (it expects one).
    if (!ark.roots.some(isDriveRoot)) return [];
    return [{ kind: 'ark.drive_stale', code: KIND_CODES['ark.drive_stale'], severity: 'warning', title: 'Ark drive has never been synced', details: { days: null } }];
  }
  if (days <= driveStaleDays) return [];
  return [
    {
      kind: 'ark.drive_stale',
      code: KIND_CODES['ark.drive_stale'],
      severity: days > driveCriticalDays ? 'critical' : 'warning',
      title: `Ark drive not synced for ${plural(days, 'day')}`,
      details: { days, lastSyncedAt: ark.driveLastSyncedAt?.toISOString() ?? null },
    },
  ];
}

function syncFailed(node: RuleNode, active: ReadonlyMap<string, ActiveAlertView>): DesiredAlert[] {
  const sync = isRecord(node.lastState) && isRecord(node.lastState.sync) ? node.lastState.sync : null;
  const lastRun = sync && isRecord(sync.last_run) ? sync.last_run : null;
  // No run on record (the history was reset) proves nothing either way: nothing to alert on.
  if (lastRun === null) return [];
  const result = asString(lastRun.result);
  if (result === 'skipped') return held(active, 'sync.failed');
  if (result !== 'failed') return [];
  const errorCode = asString(lastRun.error_code);
  const shownCode = errorCode !== null && DOCTOR_CODE_PATTERN.test(errorCode) ? errorCode : null;
  return [
    {
      kind: 'sync.failed',
      code: KIND_CODES['sync.failed'],
      severity: 'warning',
      title: shownCode ? `Last ark sync failed (${shownCode})` : 'Last ark sync failed',
      details: { runId: asString(lastRun.id), errorCode: shownCode },
    },
  ];
}

function drillFailed(node: RuleNode): DesiredAlert[] {
  const drill = isRecord(node.lastState) && isRecord(node.lastState.drill) ? node.lastState.drill : null;
  if (drill === null) return [];
  const result = asString(drill.result);
  if (result !== 'fail' && result !== 'warn') return [];
  const failed = (Array.isArray(drill.failed_checks) ? drill.failed_checks : []).filter((code): code is string => typeof code === 'string' && DOCTOR_CODE_PATTERN.test(code));
  const first = failed[0];
  const base = result === 'fail' ? 'Monthly drill failed' : 'Monthly drill finished with warnings';
  return [
    {
      kind: 'drill.failed',
      code: KIND_CODES['drill.failed'],
      severity: result === 'fail' ? 'critical' : 'warning',
      title: first ? `${base}: ${first}` : base,
      details: { result, failedChecks: failed.slice(0, 10) },
    },
  ];
}

function tier1Missing(node: RuleNode): DesiredAlert[] {
  const ark = arkOf(node.lastState);
  if (ark === null) return [];
  const bad = ark.roots
    .filter((root) => isRecord(root.tiers) && root.tiers.t1 !== 'ok')
    .map((root) => asString(root.id))
    .filter((id): id is string => id !== null);
  if (bad.length === 0) return [];
  return [
    {
      kind: 'ark.tier1_missing',
      code: KIND_CODES['ark.tier1_missing'],
      severity: 'critical',
      title: `Essential ark content is missing on ${bad.slice(0, 3).join(', ')}`,
      details: { roots: bad.slice(0, 9) },
    },
  ];
}

function keyMissing(node: RuleNode, ctx: RuleContext): DesiredAlert[] {
  if (node.phase !== 'enrolled' || node.keyReportedAt !== null || node.enrolledAt === null) return [];
  if (ageSeconds(node.enrolledAt, ctx.now) < KEY_MISSING_GRACE_MINUTES * MINUTE_S) return [];
  return [
    {
      kind: 'key.missing',
      code: KIND_CODES['key.missing'],
      severity: 'warning',
      title: "The cloud has no API key for this node, so the gateway can't use it",
      details: { enrolledAt: node.enrolledAt.toISOString() },
    },
  ];
}

function catalogBehind(node: RuleNode, ctx: RuleContext): DesiredAlert[] {
  const approved = ctx.approvedCatalog;
  if (approved === null || approved.approvedAt === null || node.catalogVersion === null) return [];
  // A node held on a catalog by an admin's pin is exactly where it was told to be.
  if (node.catalogPin !== null) return [];
  if (node.catalogVersion === approved.version) return [];
  const behindDays = ageSeconds(approved.approvedAt, ctx.now) / DAY_S;
  if (behindDays < ctx.settings.catalogBehindDays) return [];
  return [
    {
      kind: 'catalog.behind',
      code: KIND_CODES['catalog.behind'],
      severity: 'info',
      title: `Catalog ${node.catalogVersion} is behind the approved ${approved.version}`,
      details: { nodeVersion: node.catalogVersion, approvedVersion: approved.version },
    },
  ];
}

function agentOutdated(node: RuleNode, ctx: RuleContext): DesiredAlert[] {
  const latest = ctx.settings.agentLatestVersion;
  if (!isAgentBehind(node.agentVersion, latest)) return [];
  return [
    {
      kind: 'agent.outdated',
      code: KIND_CODES['agent.outdated'],
      severity: 'info',
      title: `Agent ${node.agentVersion} is behind ${latest}`,
      details: { agentVersion: node.agentVersion, latestVersion: latest },
    },
  ];
}

/** Critical alerts the node reported in its own snapshot: one per distinct code, the node's own code and title. */
function nodeReported(node: RuleNode): DesiredAlert[] {
  const alerts = isRecord(node.lastState) && Array.isArray(node.lastState.alerts) ? node.lastState.alerts : [];
  const seen = new Set<string>();
  const desired: DesiredAlert[] = [];
  for (const entry of alerts) {
    if (!isRecord(entry) || entry.severity !== 'critical') continue;
    const code = asString(entry.code);
    if (code === null || !DOCTOR_CODE_PATTERN.test(code) || seen.has(code)) continue;
    seen.add(code);
    const title = (asString(entry.title) ?? '').trim().slice(0, 200) || `The node reported ${code}`;
    desired.push({ kind: `${NODE_REPORTED_PREFIX}${code}`, code, severity: 'critical', title, details: { reportedBy: 'node' } });
  }
  return desired;
}

/**
 * The alerts that are true for `node` at `ctx.now`. A `pending` node (not enrolled yet) has nothing to be wrong about.
 * `active` is the node's currently active alerts by kind, used only to hold the two silence kinds and `sync.failed`.
 */
export function evaluateNodeAlerts(node: RuleNode, ctx: RuleContext, active: ReadonlyMap<string, ActiveAlertView> = new Map()): DesiredAlert[] {
  if (node.phase !== 'enrolled') return [];
  const health = deriveHealth({ eab: node.eab, phase: node.phase, mode: node.mode, lastStateAt: node.lastStateAt, now: ctx.now });
  return [
    ...nodeStale(node, health, ctx, active),
    ...nodeOffline(node, health, ctx, active),
    ...driveStale(node, ctx),
    ...syncFailed(node, active),
    ...drillFailed(node),
    ...tier1Missing(node),
    ...keyMissing(node, ctx),
    ...catalogBehind(node, ctx),
    ...agentOutdated(node, ctx),
    ...nodeReported(node),
  ];
}
