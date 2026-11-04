// Queues `catalog.refresh` on the nodes that can take it right now (approval with `notifyNodes`, docs/specs/cloud-app.md
// section 4.6). One command per eligible node, deduplicated by the command plane's active-dedup index
// (`localai_commands_active_dedup`), so approving twice does not stack two commands on a node.
//
// INTERIM: the command plane proper (`CommandsService.enqueue`, section 4.4, CLOUD-08) validates params, checks the
// requester's permission and publishes the wake-up. Until it lands this class writes the same row and publishes the
// same event itself; the row it writes is exactly what `CommandsService.enqueue('catalog.refresh', {})` writes, so the
// swap is a one-line change in `CatalogService`.
import { Inject, Injectable, Logger } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';
import { SystemSettingsService } from '@marinoscar/platform-api/settings';

import { PrismaService } from '../../prisma/prisma.service';
import {
  COMMAND_CATALOG_REFRESH,
  COMMAND_CATALOG_REFRESH_DEDUP,
  COMMAND_CATALOG_REFRESH_TTL_MS,
} from './catalog.constants';
import { isCatalogNotifyEligible } from './node-eligibility';

export interface RefreshQueueResult {
  /** Lab node ids a new command was queued for. */
  queued: string[];
  /** Lab node ids that already had an active `catalog.refresh` (nothing new was queued). */
  alreadyQueued: string[];
  /** Enrolled nodes left out because they are quiet, down or offline. */
  skipped: number;
}

function isUniqueViolation(error: unknown): boolean {
  return typeof error === 'object' && error !== null && (error as { code?: string }).code === 'P2002';
}

@Injectable()
export class CatalogRefreshQueue {
  private readonly logger = new Logger(CatalogRefreshQueue.name);

  constructor(
    private readonly prisma: PrismaService,
    private readonly settings: SystemSettingsService,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
    @Inject(EVENT_BUS) private readonly bus: EventBus,
  ) {}

  async queueForEligibleNodes(requestedById: string | null, now: Date = new Date()): Promise<RefreshQueueResult> {
    const nodes = await this.prisma.localaiNode.findMany({ where: { phase: 'enrolled' } });
    const workerIds = nodes.flatMap((node) => (node.workerNodeId ? [node.workerNodeId] : []));
    const workers = workerIds.length
      ? await this.prisma.workerNode.findMany({ where: { id: { in: workerIds } }, select: { id: true, status: true, lastHeartbeatAt: true } })
      : [];
    const workerById = new Map(workers.map((worker) => [worker.id, worker]));
    const { staleHeartbeatSeconds } = await this.settings.getNodesPolicy();

    const result: RefreshQueueResult = { queued: [], alreadyQueued: [], skipped: 0 };
    for (const node of nodes) {
      const worker = node.workerNodeId ? (workerById.get(node.workerNodeId) ?? null) : null;
      if (!isCatalogNotifyEligible(node, worker, staleHeartbeatSeconds, now)) {
        result.skipped += 1;
        continue;
      }
      try {
        const command = await this.prisma.localaiCommand.create({
          data: {
            nodeId: node.id,
            type: COMMAND_CATALOG_REFRESH,
            params: {},
            dedupKey: COMMAND_CATALOG_REFRESH_DEDUP,
            source: 'catalog_approve',
            requestedById,
            expiresAt: new Date(now.getTime() + COMMAND_CATALOG_REFRESH_TTL_MS),
          },
        });
        result.queued.push(node.nodeId);
        await this.announce(requestedById, command.id, node.nodeId);
      } catch (error) {
        if (!isUniqueViolation(error)) throw error;
        result.alreadyQueued.push(node.nodeId);
      }
    }
    return result;
  }

  /** After the insert committed: audit and wake the node's long poll. Neither can fail the approval. */
  private async announce(actorUserId: string | null, commandId: string, nodeId: string): Promise<void> {
    try {
      await this.audit.record({
        action: 'localai.command.requested',
        actorUserId,
        targetType: 'localai_command',
        targetId: commandId,
        meta: { nodeId, type: COMMAND_CATALOG_REFRESH },
      });
    } catch (error) {
      this.logger.error(`audit localai.command.requested failed for node_id=${nodeId}: ${error instanceof Error ? error.name : 'unknown error'}`);
    }
    try {
      await this.bus.publish('localai.command.enqueued', { nodeId });
    } catch (error) {
      this.logger.warn(`wake-up for node_id=${nodeId} not published: ${error instanceof Error ? error.name : 'unknown error'}`);
    }
  }
}
