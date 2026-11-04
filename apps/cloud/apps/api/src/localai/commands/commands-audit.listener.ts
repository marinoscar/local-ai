// The audit and notification side of a settled command (docs/specs/cloud-app.md sections 3.5 and 4.9).
//
// It listens to `localai.command.settled`, which the command plane publishes ONCE per command, after the settling
// write committed (a node's result or failure, or the sweeper's expiry or lost lease). For each event it
//   1. writes `localai.command.settled` { nodeId, type, status, errorCode? } through the platform's `AUDIT_SINK`
//      (the result is never read, so a `logs.tail` result can never reach the audit table), and
//   2. for `failed` and `expired` with a requester, tells the notifier, which raises `localai.command.failed` to that
//      person: "{type} on {node} failed: {fix}".
// Both run after the commit and outside any transaction. A failure of either is logged (the error name only) and
// never breaks the bus, the other step or the settled command. Handling is idempotent per command: if the settled
// audit row already exists (a redelivery, or a second replica on the cross-replica bus that got there first) nothing
// is written or sent again.
import { Inject, Injectable, Logger, type OnModuleDestroy, type OnModuleInit } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';

import { PrismaService } from '../../prisma/prisma.service';
import { AUDIT_COMMAND_SETTLED, AUDIT_COMMAND_TARGET } from './command-audit';
import { COMMAND_SETTLED_CHANNEL, type CommandSettledEvent } from './command-events';
import { fixForCommandError } from './command-failure-fix';
import { LOCALAI_COMMAND_NOTIFIER, type LocalaiCommandNotifier } from './command-notifier';

const FAILED_STATUSES: ReadonlySet<string> = new Set(['failed', 'expired']);

@Injectable()
export class CommandsAuditListener implements OnModuleInit, OnModuleDestroy {
  private readonly logger = new Logger(CommandsAuditListener.name);
  private unsubscribe: (() => void) | null = null;

  constructor(
    private readonly prisma: PrismaService,
    @Inject(EVENT_BUS) private readonly bus: EventBus,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
    @Inject(LOCALAI_COMMAND_NOTIFIER) private readonly notifier: LocalaiCommandNotifier,
  ) {}

  onModuleInit(): void {
    this.unsubscribe = this.bus.subscribe<CommandSettledEvent>(COMMAND_SETTLED_CHANNEL, (event) => {
      void this.handleSettled(event);
    });
  }

  onModuleDestroy(): void {
    this.unsubscribe?.();
    this.unsubscribe = null;
  }

  /** Audits one settled command and notifies its requester on a failure. Never throws. */
  async handleSettled(event: CommandSettledEvent): Promise<void> {
    try {
      await this.settle(event);
    } catch (error) {
      this.logger.error(`handling ${COMMAND_SETTLED_CHANNEL} failed for command_id=${event?.commandId} (${error instanceof Error ? error.name : 'error'})`);
    }
  }

  private async settle(event: CommandSettledEvent): Promise<void> {
    if (!event || typeof event.commandId !== 'string') return;
    // Only the columns the audit row and the message need: never `result`, so no log line can reach either.
    const row = await this.prisma.localaiCommand.findUnique({
      where: { id: event.commandId },
      select: { id: true, type: true, status: true, error: true, requestedById: true, node: { select: { nodeId: true, displayName: true } } },
    });
    if (!row || !['succeeded', 'failed', 'expired'].includes(row.status)) return;

    const already = await this.prisma.auditEvent.findFirst({
      where: { action: AUDIT_COMMAND_SETTLED, targetType: AUDIT_COMMAND_TARGET, targetId: row.id },
      select: { id: true },
    });
    if (already) return;

    const error = (row.error ?? null) as { code?: string; message?: string } | null;
    const errorCode = row.status !== 'succeeded' && typeof error?.code === 'string' ? error.code : null;
    try {
      await this.audit.record({
        action: AUDIT_COMMAND_SETTLED,
        actorUserId: row.requestedById,
        targetType: AUDIT_COMMAND_TARGET,
        targetId: row.id,
        meta: { nodeId: row.node.nodeId, type: row.type, status: row.status, ...(errorCode ? { errorCode } : {}) },
      });
    } catch (failure) {
      this.logger.error(`audit ${AUDIT_COMMAND_SETTLED} failed for command_id=${row.id} (${failure instanceof Error ? failure.name : 'error'})`);
    }

    if (!FAILED_STATUSES.has(row.status) || !row.requestedById) return;
    const type = row.type.replace(/^localai\./, '');
    const fix = fixForCommandError(error);
    const nodeName = row.node.displayName || row.node.nodeId;
    try {
      await this.notifier.commandFailed({
        recipientUserId: row.requestedById,
        commandId: row.id,
        nodeId: row.node.nodeId,
        nodeName,
        type,
        status: row.status as 'failed' | 'expired',
        errorCode,
        fix,
        message: `${type} on ${nodeName} failed: ${fix}`,
      });
    } catch (failure) {
      this.logger.error(`notification ${row.id} failed (${failure instanceof Error ? failure.name : 'error'})`);
    }
  }
}
