// Which nodes `notifyNodes` pings (docs/specs/cloud-app.md section 4.6: "each `routable`-or-`degraded` node").
//
// This is the derived health of section 4.5 reduced to the question the approval asks: is this node reachable
// enough that a `catalog.refresh` command would be claimed soon? A node in expected silence (Offline or Survival
// mode, or still `pending`) is skipped on purpose: the command would only expire unclaimed (LAI-CLD-034). `retired`
// nodes are skipped too. The node picks the catalog up on its nightly sync anyway.
//
// Pure: the clock and the stale window are arguments. CLOUD-06 owns the full health chip for the UI; when it lands
// this function should call its derivation instead of repeating the thresholds.
import { deriveNodeHealth } from '@marinoscar/platform-api/nodes';
import type { LocalaiNode } from '@prisma/client';

/** The newest snapshot age (seconds) that still counts as `healthy` (section 4.5). */
export const HEALTHY_SNAPSHOT_MAX_AGE_S = 120;
/** The newest snapshot age (seconds) that still counts as `degraded` (section 4.5). */
export const DEGRADED_SNAPSHOT_MAX_AGE_S = 600;

export type NodeHealthClass = 'quiet' | 'healthy' | 'degraded' | 'down';

type NodeFacts = Pick<LocalaiNode, 'phase' | 'mode' | 'lastState' | 'lastStateAt'>;

export interface WorkerFacts {
  status: string;
  lastHeartbeatAt: Date | null;
}

function reportedMode(node: NodeFacts): string | null {
  if (node.mode) return node.mode;
  const state = node.lastState;
  if (state && typeof state === 'object' && !Array.isArray(state) && typeof state.mode === 'string') return state.mode;
  return null;
}

/** The derived health of section 4.5 (`quiet | healthy | degraded | down`). */
export function classifyNodeHealth(node: NodeFacts, worker: WorkerFacts | null, staleHeartbeatSeconds: number, now: Date): NodeHealthClass {
  const mode = reportedMode(node);
  if (node.phase === 'pending' || mode === 'offline' || mode === 'survival') return 'quiet';
  if (!worker || !node.lastStateAt) return 'down';
  const eab = deriveNodeHealth(worker, staleHeartbeatSeconds, now);
  const ageS = (now.getTime() - node.lastStateAt.getTime()) / 1000;
  if (eab === 'healthy' && ageS <= HEALTHY_SNAPSHOT_MAX_AGE_S) return 'healthy';
  if (eab !== 'offline' && ageS <= DEGRADED_SNAPSHOT_MAX_AGE_S) return 'degraded';
  return 'down';
}

/** True for an enrolled node whose health is `healthy` or `degraded`. */
export function isCatalogNotifyEligible(node: NodeFacts, worker: WorkerFacts | null, staleHeartbeatSeconds: number, now: Date): boolean {
  if (node.phase !== 'enrolled') return false;
  const health = classifyNodeHealth(node, worker, staleHeartbeatSeconds, now);
  return health === 'healthy' || health === 'degraded';
}
