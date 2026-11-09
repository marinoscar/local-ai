// What the node reports about its own place on the tailnet (`POST .../localai/tailnet`, issue #437). The values land
// on the node row the wizard and the publish checklist read, so nobody types them. Idempotent: the same report again
// changes nothing and writes no audit row.
//
// The address is admin-only data: it is audited as the word `<set>` (or `<changed>` when a stored one is replaced),
// never as a value, and it is not logged.
import { Inject, Injectable, Logger } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import type { LocalaiNode } from '@prisma/client';

import { PrismaService } from '../../prisma/prisma.service';
import type { NodeTailnetRequest, NodeTailnetResponse } from '../contracts/tailnet.schema';
import { AUDIT_NODE_TARGET } from '../nodes/lab-nodes.service';
import { AUDIT_NODE_UPDATED } from '../nodes/nodes-admin.service';

@Injectable()
export class NodeTailnetService {
  private readonly logger = new Logger(NodeTailnetService.name);

  constructor(
    private readonly prisma: PrismaService,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
  ) {}

  async report(node: LocalaiNode, input: NodeTailnetRequest, actorUserId: string | null): Promise<NodeTailnetResponse> {
    const changes: Record<string, string> = {};
    const data: { tailnetIp?: string; tailnetMachine?: string } = {};
    if (input.ip !== node.tailnetIp) {
      data.tailnetIp = input.ip;
      changes.tailnetIp = node.tailnetIp === null ? '<set>' : '<changed>';
    }
    if (input.machine !== undefined && input.machine !== node.tailnetMachine) {
      data.tailnetMachine = input.machine;
      changes.tailnetMachine = input.machine;
    }
    if (Object.keys(changes).length === 0) return { stored: false };
    await this.prisma.localaiNode.update({ where: { id: node.id }, data });
    try {
      await this.audit.record({
        action: AUDIT_NODE_UPDATED,
        actorUserId,
        targetType: AUDIT_NODE_TARGET,
        targetId: node.nodeId,
        meta: { ...changes, source: 'node' },
      });
    } catch (error) {
      this.logger.error(`recording ${AUDIT_NODE_UPDATED} for ${node.nodeId} failed (${error instanceof Error ? error.name : 'error'})`);
    }
    return { stored: true };
  }
}
