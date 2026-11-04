// The read side of `GET /api/localai/nodes` and `GET /api/localai/nodes/:nodeId` (docs/specs/cloud-app.md section
// 3.9): gathers the rows (nodes, projected models, worker heartbeats, active alerts, the approved catalog), then hands
// them to the pure builder. Read-only; writes nothing and publishes nothing.
import { Injectable, NotFoundException } from '@nestjs/common';
import { deriveNodeHealth, type NodeHealth } from '@marinoscar/platform-api/nodes';

import { PrismaService } from '../../prisma/prisma.service';
import type { LabNodesResponse, NodeCardDto, NodeDetailDto } from './dto/node-card';
import { LabSettingsService } from './lab-settings.service';
import { buildNodeCard, compareNodeIds, summarize, type CardContext, type CardNodeRow, type CardSource } from './node-card.builder';

@Injectable()
export class LabNodesService {
  constructor(
    private readonly prisma: PrismaService,
    private readonly settings: LabSettingsService,
  ) {}

  /** Every node that is not retired, as cards, with the header counts. */
  async list(now: Date = new Date()): Promise<LabNodesResponse> {
    const rows = await this.prisma.localaiNode.findMany({ where: { phase: { not: 'retired' } } });
    const { cards, labWideAlerts } = await this.cardsFor(rows, now);
    cards.sort((a, b) => compareNodeIds(a.nodeId, b.nodeId));
    return { summary: summarize(cards, labWideAlerts), nodes: cards };
  }

  /** One node: the card fields plus `lastState`. `tailnetIp` is added only when `revealTailnet` (the controller checks lab:admin). */
  async detail(nodeId: string, revealTailnet: boolean, now: Date = new Date()): Promise<NodeDetailDto> {
    const row = await this.prisma.localaiNode.findUnique({ where: { nodeId } });
    if (!row || row.phase === 'retired') throw new NotFoundException({ message: 'No Local AI node has this id' });
    const { cards } = await this.cardsFor([row], now);
    const detail: NodeDetailDto = { ...cards[0]!, lastState: row.lastState ?? null };
    if (revealTailnet) detail.tailnetIp = row.tailnetIp ?? null;
    return detail;
  }

  private async cardsFor(
    rows: ReadonlyArray<CardNodeRow & { id: string; workerNodeId: string | null }>,
    now: Date,
  ): Promise<{ cards: NodeCardDto[]; labWideAlerts: number }> {
    const settings = await this.settings.read();
    const ids = rows.map((row) => row.id);
    const workerIds = rows.map((row) => row.workerNodeId).filter((id): id is string => id !== null);

    const [models, workers, alertGroups, approved] = await Promise.all([
      ids.length ? this.prisma.localaiNodeModel.findMany({ where: { nodeId: { in: ids } }, select: { nodeId: true, modelId: true, status: true, isDefault: true } }) : [],
      workerIds.length ? this.prisma.workerNode.findMany({ where: { id: { in: workerIds } }, select: { id: true, status: true, lastHeartbeatAt: true } }) : [],
      this.prisma.localaiAlert.groupBy({ by: ['nodeId', 'severity'], where: { state: 'active' }, _count: { _all: true } }),
      this.prisma.localaiCatalogVersion.findFirst({ where: { status: 'approved' }, select: { version: true } }),
    ]);

    const context: CardContext = {
      now,
      thresholds: settings.thresholds,
      agentLatestVersion: settings.agentLatestVersion,
      approvedCatalogVersion: approved?.version ?? null,
    };

    const workerById = new Map(workers.map((worker) => [worker.id, worker]));
    const alertsByNode = new Map<string, { active: number; critical: number }>();
    let labWideAlerts = 0;
    for (const group of alertGroups) {
      const count = group._count._all;
      if (group.nodeId === null) {
        labWideAlerts += count;
        continue;
      }
      const entry = alertsByNode.get(group.nodeId) ?? { active: 0, critical: 0 };
      entry.active += count;
      if (group.severity === 'critical') entry.critical += count;
      alertsByNode.set(group.nodeId, entry);
    }

    const cards = rows.map((row) => {
      const worker = row.workerNodeId ? workerById.get(row.workerNodeId) : undefined;
      // A node with no linked worker row counts as offline (spec 4.5).
      const eab: NodeHealth = worker ? deriveNodeHealth(worker, settings.staleHeartbeatSeconds, now) : 'offline';
      const source: CardSource = {
        node: row,
        models: models.filter((model) => model.nodeId === row.id),
        eab,
        alerts: alertsByNode.get(row.id) ?? { active: 0, critical: 0 },
      };
      return buildNodeCard(source, context);
    });
    return { cards, labWideAlerts };
  }
}
