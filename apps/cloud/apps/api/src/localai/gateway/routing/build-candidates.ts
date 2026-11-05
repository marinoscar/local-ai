// Builds the routing snapshot from the stored rows (docs/specs/gateway.md section 4.3). PURE: no I/O, no clock (the
// caller passes `now`). The health and the routable verdict are the cloud's own (`health/derive-health.ts`, the Lab
// page uses the same functions); nothing is re-derived here.
import type { NodeHealth } from '@marinoscar/platform-api/nodes';

import { deriveHealth, isGatewayRoutable, type LabMode, type LabPhase, type LabServePattern } from '../../health/derive-health';
import type { Candidate } from './routing.types';

export interface SourceModelRow {
  modelId: string;
  present: boolean;
  verified: boolean;
  loadable: boolean;
  status: string;
  slots: number;
  ctx: number | null;
  inputModalities?: readonly string[];
}

/** A `localai_nodes` row with its models and the EAB health of its linked worker (`offline` when it has none). */
export interface SourceNodeRow {
  nodeId: string;
  publicHost: string;
  servePattern: LabServePattern;
  tailnetIp: string | null;
  tailnetMachine: string | null;
  gatewayPriority: number;
  gatewayEnabled: boolean;
  phase: LabPhase;
  mode: string | null;
  lastStateAt: Date | null;
  keyReportedAt: Date | null;
  eab: NodeHealth;
  /** `llama.models_max` of the node's last snapshot (how many models its router keeps loaded); null when it reported none. */
  modelsMax?: number | null;
  models: readonly SourceModelRow[];
}

export interface RoutingSnapshot {
  /** Candidates by model id, in no particular order. */
  candidates: Map<string, Candidate[]>;
  /** The nodes that passed the `gatewayRoutable` filter. */
  routable: Map<string, { nodeId: string; publicHost: string }>;
  /** Models present on any node that is not retired, routable or not (the 503 LAI-GW-021 versus 404 LAI-GW-020 question). */
  present: Set<string>;
  /** `llama.models_max` by routable node id, for the on-demand load's eviction guard (gateway.md section 4.3 step 3). */
  modelsMax: Map<string, number>;
}

const MODES: readonly string[] = ['online', 'offline', 'survival'];

function asMode(value: string | null): LabMode | null {
  return value !== null && MODES.includes(value) ? (value as LabMode) : null;
}

export function buildSnapshot(rows: readonly SourceNodeRow[], now: Date): RoutingSnapshot {
  const candidates = new Map<string, Candidate[]>();
  const routable = new Map<string, { nodeId: string; publicHost: string }>();
  const present = new Set<string>();
  const modelsMax = new Map<string, number>();

  for (const row of rows) {
    if (row.phase === 'retired') continue;
    for (const model of row.models) if (model.present) present.add(model.modelId);

    const mode = asMode(row.mode);
    const health = deriveHealth({ eab: row.eab, phase: row.phase, mode, lastStateAt: row.lastStateAt, now });
    const admitted = isGatewayRoutable({
      health,
      mode,
      phase: row.phase,
      gatewayEnabled: row.gatewayEnabled,
      servePattern: row.servePattern,
      keyReportedAt: row.keyReportedAt,
    });
    if (!admitted || row.servePattern === 'none') continue;
    routable.set(row.nodeId, { nodeId: row.nodeId, publicHost: row.publicHost });
    if (typeof row.modelsMax === 'number' && Number.isInteger(row.modelsMax) && row.modelsMax >= 1) modelsMax.set(row.nodeId, row.modelsMax);

    for (const model of row.models) {
      const canLoad = model.present && model.verified && model.loadable;
      let status: Candidate['status'] | null = null;
      if (model.status === 'loaded') status = 'loaded';
      else if (canLoad && model.status === 'unloaded') status = 'loadable';
      if (status === null) continue;
      const list = candidates.get(model.modelId) ?? [];
      list.push({
        nodeId: row.nodeId,
        modelId: model.modelId,
        publicHost: row.publicHost,
        servePattern: row.servePattern,
        tailnetIp: row.tailnetIp,
        tailnetMachine: row.tailnetMachine,
        priority: row.gatewayPriority,
        status,
        canLoad,
        slots: Math.max(1, model.slots),
        ctx: model.ctx,
        inputModalities: model.inputModalities ?? [],
      });
      candidates.set(model.modelId, list);
    }
  }
  return { candidates, routable, present, modelsMax };
}
