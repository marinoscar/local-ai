// Shared builders of the alert unit tests (not production code): a node, a snapshot and a context at a fixed instant.
import { DEFAULT_ALERT_SETTINGS, type AlertSettings } from './alert-settings';
import type { RuleContext, RuleNode } from './alert-rules';

export const NOW = new Date('2026-11-04T12:00:00.000Z');
export const MIN = 60_000;
export const HOUR = 3_600_000;
export const DAY = 86_400_000;
export const ago = (ms: number, from: Date = NOW): Date => new Date(from.getTime() - ms);

export function settings(patch: Partial<AlertSettings> = {}): AlertSettings {
  return { ...DEFAULT_ALERT_SETTINGS, staleHeartbeatSeconds: 45, ...patch };
}

export function context(patch: Partial<RuleContext> = {}): RuleContext {
  return { now: NOW, settings: settings(), approvedCatalog: null, ...patch };
}

/** A snapshot as the ingestion stores it, reduced to what the rules read. */
export function state(patch: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    mode: 'online',
    ark: { roots: [{ id: 'internal', present: true, tiers: { t1: 'ok', t2: 'ok', t3: 'ok' } }], drive_last_synced_at: null },
    sync: { running: false, last_run: { id: 'run-1', finished_at: NOW.toISOString(), result: 'ok', skip_reason: null, error_code: null } },
    drill: { run_id: 'drill-1', last_run_at: NOW.toISOString(), result: 'pass', survival_ready: true, failed_checks: [], failed_check_ids: [] },
    alerts: [],
    ...patch,
  };
}

/** A healthy, enrolled, online node whose snapshot is 5 seconds old; override what a test is about. */
export function node(patch: Partial<RuleNode> = {}): RuleNode {
  return {
    nodeId: 'ai-n1',
    phase: 'enrolled',
    mode: 'online',
    lastStateAt: ago(5_000),
    lastState: state(),
    agentVersion: '0.4.1',
    catalogVersion: '2026.11.02-1',
    catalogPin: null,
    keyReportedAt: ago(DAY),
    eab: 'healthy',
    lastContactAt: ago(5_000),
    enrolledAt: ago(30 * DAY),
    ...patch,
  };
}
