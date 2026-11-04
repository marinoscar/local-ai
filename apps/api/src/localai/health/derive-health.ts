// Derived node health (docs/specs/cloud-app.md section 4.5). PURE: no I/O, no clock (the caller passes `now`), so the
// whole table is tested over (EAB health x snapshot age x mode x phase) in `derive-health.spec.ts`.
//
// Computed at read time and NEVER stored, for the reason EAB gives for worker nodes: a stored verdict is correct when
// written and rots silently after. The web app never recomputes it; it renders what `GET /api/localai/nodes` returns.
import type { NodeHealth } from '@marinoscar/platform-api/nodes';

/** The four verdicts a node card shows. `quiet` is expected silence (offline/survival mode, or not enrolled yet). */
export type LabHealth = 'healthy' | 'degraded' | 'down' | 'quiet';
export type LabMode = 'online' | 'offline' | 'survival';
export type LabPhase = 'pending' | 'enrolled' | 'retired';
export type LabServePattern = 'A' | 'B' | 'none';

/** A snapshot this old (or younger) with a healthy EAB heartbeat is `healthy` (spec 4.5: 120 s). */
export const HEALTHY_MAX_AGE_SECONDS = 120;
/** A snapshot this old (or younger) is `degraded` rather than `down`, unless EAB says offline (spec 4.5: 600 s). */
export const DEGRADED_MAX_AGE_SECONDS = 600;

export interface HealthInput {
  /** EAB's `deriveNodeHealth(workerNode)`; a node with no linked worker row counts as `offline` (the caller maps it). */
  eab: NodeHealth;
  phase: LabPhase;
  /** `lastState.mode`, or null before the first snapshot. */
  mode: LabMode | null;
  /** When the last snapshot was accepted, or null before the first one. */
  lastStateAt: Date | null;
  now: Date;
}

export interface RoutableInput {
  health: LabHealth;
  mode: LabMode | null;
  phase: LabPhase;
  gatewayEnabled: boolean;
  servePattern: LabServePattern;
  /** `keyReportedAt`: when the node last reported its llama key; null means the gateway holds no key for it. */
  keyReportedAt: Date | null;
}

/** Whole seconds between two instants; `Infinity` when there is no snapshot (never reported). Never negative. */
export function ageSeconds(lastStateAt: Date | null, now: Date): number {
  if (lastStateAt === null) return Number.POSITIVE_INFINITY;
  return Math.max(0, (now.getTime() - lastStateAt.getTime()) / 1000);
}

/**
 * quiet    expected silence: the last reported mode is offline or survival, or the node is still `pending`
 * healthy  EAB healthy and the snapshot is at most 120 s old
 * degraded EAB not offline and the snapshot is at most 600 s old
 * down     everything else
 *
 * The age boundaries are inclusive: exactly 120 s is healthy, 120.001 s is degraded; exactly 600 s is degraded.
 */
export function deriveHealth(input: HealthInput): LabHealth {
  const expectedSilence = input.phase === 'pending' || input.mode === 'offline' || input.mode === 'survival';
  if (expectedSilence) return 'quiet';
  const age = ageSeconds(input.lastStateAt, input.now);
  if (input.eab === 'healthy' && age <= HEALTHY_MAX_AGE_SECONDS) return 'healthy';
  if (input.eab !== 'offline' && age <= DEGRADED_MAX_AGE_SECONDS) return 'degraded';
  return 'down';
}

/**
 * What the UI counts as "Gateway: N nodes". `healthy` already needs a fresh bound snapshot, so a leaked credential
 * faking EAB heartbeats for another node cannot make it routable (spec 4.5).
 */
export function isRoutable(input: RoutableInput): boolean {
  return (
    input.health === 'healthy' &&
    input.mode === 'online' &&
    input.gatewayEnabled &&
    input.phase === 'enrolled' &&
    input.servePattern !== 'none'
  );
}

/**
 * What the gateway routing table admits: `routable` AND a stored llama key (the gateway cannot call a node whose key
 * it does not hold; gateway.md sections 2 and 4.3). Exported for GW-04, which builds its table from it.
 */
export function isGatewayRoutable(input: RoutableInput): boolean {
  return isRoutable(input) && input.keyReportedAt !== null;
}
