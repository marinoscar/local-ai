// Where the routing table reads from: the database. A port (`ROUTING_SOURCE`) so the table is tested with a fake and
// the database tier proves the real query. Reads only; the table never writes.
import { Injectable, Logger } from '@nestjs/common';
import { deriveNodeHealth, type NodeHealth } from '@marinoscar/platform-api/nodes';

import { PrismaService } from '../../../prisma/prisma.service';
import { LabSettingsService } from '../../nodes/lab-settings.service';
import type { LabPhase, LabServePattern } from '../../health/derive-health';
import type { SourceNodeRow } from './build-candidates';
import { DEFAULT_QUEUE_FACTOR, type RoutingSettings } from './routing.types';

export interface RoutingSourceData {
  rows: SourceNodeRow[];
  settings: RoutingSettings;
}

export interface RoutingSource {
  load(now: Date): Promise<RoutingSourceData>;
}

/** Injection token of the {@link RoutingSource}. */
export const ROUTING_SOURCE = Symbol('LOCALAI_GATEWAY_ROUTING_SOURCE');

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

/**
 * `gateway.queueFactor` from the stored `localai` settings object: the nested form (`{ gateway: { queueFactor } }`) or
 * the dotted key (`{ 'gateway.queueFactor': n }`), as the other `localai` readers accept. A value that is not an integer
 * from 1 to 64 falls back to the default (4). Pure, for the unit test.
 */
export function parseRoutingSettings(localai: unknown): RoutingSettings {
  const stored = isRecord(localai) ? localai : {};
  const nested = isRecord(stored.gateway) ? stored.gateway.queueFactor : undefined;
  const value = nested ?? stored['gateway.queueFactor'];
  const queueFactor = typeof value === 'number' && Number.isInteger(value) && value >= 1 && value <= 64 ? value : DEFAULT_QUEUE_FACTOR;
  return { queueFactor };
}

@Injectable()
export class PrismaRoutingSource implements RoutingSource {
  private readonly logger = new Logger(PrismaRoutingSource.name);

  constructor(
    private readonly prisma: PrismaService,
    private readonly lab: LabSettingsService,
  ) {}

  async load(now: Date): Promise<RoutingSourceData> {
    const [nodes, labSettings, queueFactor] = await Promise.all([
      this.prisma.localaiNode.findMany({ where: { phase: { not: 'retired' } }, include: { models: true } }),
      this.lab.read(),
      this.readSettings(),
    ]);
    const workerIds = nodes.map((node) => node.workerNodeId).filter((id): id is string => id !== null);
    const workers = workerIds.length
      ? await this.prisma.workerNode.findMany({ where: { id: { in: workerIds } }, select: { id: true, status: true, lastHeartbeatAt: true } })
      : [];
    const workerById = new Map(workers.map((worker) => [worker.id, worker]));

    const rows: SourceNodeRow[] = nodes.map((node) => {
      const worker = node.workerNodeId ? workerById.get(node.workerNodeId) : undefined;
      // A node with no linked worker row counts as offline (cloud-app.md section 4.5).
      const eab: NodeHealth = worker ? deriveNodeHealth(worker, labSettings.staleHeartbeatSeconds, now) : 'offline';
      return {
        nodeId: node.nodeId,
        publicHost: node.publicHost,
        servePattern: node.servePattern as LabServePattern,
        tailnetIp: node.tailnetIp,
        tailnetMachine: node.tailnetMachine,
        gatewayPriority: node.gatewayPriority,
        gatewayEnabled: node.gatewayEnabled,
        phase: node.phase as LabPhase,
        mode: node.mode,
        lastStateAt: node.lastStateAt,
        keyReportedAt: node.keyReportedAt,
        eab,
        models: node.models.map((model) => ({
          modelId: model.modelId,
          present: model.present,
          verified: model.verified,
          loadable: model.loadable,
          status: model.status,
          slots: model.slots,
          ctx: model.ctx,
        })),
      };
    });
    return { rows, settings: queueFactor };
  }

  private async readSettings(): Promise<RoutingSettings> {
    try {
      const row = await this.prisma.systemSettings.findUnique({ where: { key: 'global' } });
      return parseRoutingSettings(isRecord(row?.value) ? row.value.localai : undefined);
    } catch (error) {
      this.logger.warn(`could not read the gateway settings, using the defaults: ${error instanceof Error ? error.name : 'unknown error'}`);
      return { queueFactor: DEFAULT_QUEUE_FACTOR };
    }
  }
}
