// The real ports of the on-demand load (`on-demand.ts`): the command plane's rows (reads only) and the audit row of a
// load the gateway queued. Everything the pure part decides is in `on-demand.ts`.
import { Inject, Injectable } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';

import { PrismaService } from '../../../prisma/prisma.service';
import { AUDIT_COMMAND_REQUESTED, AUDIT_COMMAND_TARGET } from '../../commands/command-audit';
import { dedupKeyFor } from '../../commands/command-policy';
import { ACTIVE_STATUSES } from '../../commands/command-lifecycle';
import type { ActiveLoad, ActiveLoadReader, CommandOutcome, LoadAudit } from './on-demand';

@Injectable()
export class PrismaActiveLoadReader implements ActiveLoadReader {
  constructor(private readonly prisma: PrismaService) {}

  async find(modelId: string): Promise<ActiveLoad | null> {
    const row = await this.prisma.localaiCommand.findFirst({
      where: { type: 'localai.model.load', dedupKey: dedupKeyFor('localai.model.load', { model_id: modelId }), status: { in: [...ACTIVE_STATUSES] } },
      orderBy: { createdAt: 'asc' },
      select: { id: true, node: { select: { nodeId: true } } },
    });
    return row ? { commandId: row.id, nodeId: row.node.nodeId } : null;
  }

  async outcome(commandId: string): Promise<CommandOutcome | null> {
    const row = await this.prisma.localaiCommand.findUnique({ where: { id: commandId }, select: { status: true } });
    if (!row) return null;
    if (row.status === 'queued' || row.status === 'running') return 'active';
    return row.status === 'succeeded' ? 'succeeded' : 'failed';
  }
}

/** `localai.command.requested` for a load the gateway queued: source `gateway`, the key's display prefix, no prompt. */
@Injectable()
export class AuditLoadAudit implements LoadAudit {
  constructor(@Inject(AUDIT_SINK) private readonly audit: AuditSink) {}

  async requested(input: { commandId: string; nodeId: string; params: Record<string, unknown>; keyPrefix: string | null }): Promise<void> {
    await this.audit.record({
      action: AUDIT_COMMAND_REQUESTED,
      actorUserId: null,
      targetType: AUDIT_COMMAND_TARGET,
      targetId: input.commandId,
      meta: { nodeId: input.nodeId, type: 'localai.model.load', params: JSON.stringify(input.params), source: 'gateway', keyPrefix: input.keyPrefix },
    });
  }
}
