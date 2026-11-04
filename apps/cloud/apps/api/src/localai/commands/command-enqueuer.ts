// The seam through which other Local AI features queue a command for a node (docs/specs/cloud-app.md section 3.7).
//
// The command plane (`CommandsService`, its claim routes, the sweeper) is CLOUD-08. Until it lands, this port has a
// minimal default (`PrismaCommandEnqueuer`) that inserts the row exactly as `CommandsService.enqueue` will: the same
// type name, dedup key, queued TTL and `localai_commands_active_dedup` collapse. CLOUD-08 rebinds
// `LOCALAI_COMMAND_ENQUEUER` to `CommandsService` (`useExisting`) in `CommandEnqueuerModule` and deletes the default;
// callers (the "Ask all nodes to re-send keys" action now) do not change.
import { Injectable } from '@nestjs/common';
import { Prisma } from '@prisma/client';

import { PrismaService } from '../../prisma/prisma.service';

/** Injection token of the {@link LocalaiCommandEnqueuer}. */
export const LOCALAI_COMMAND_ENQUEUER = Symbol('LOCALAI_COMMAND_ENQUEUER');

/** Who queued the command: a person's action (`ui`), the API itself (`system`) or the gateway (`gateway`). */
export type CommandSource = 'ui' | 'system' | 'gateway';

export interface EnqueuedCommand {
  /** `localai_commands.id`. */
  id: string;
  /** False when an active command of the same type and subject already existed and was returned instead. */
  created: boolean;
}

export interface LocalaiCommandEnqueuer {
  /**
   * Queues `type` (without the `localai.` prefix, for example `status.refresh`) for the node with lab id `nodeId`
   * (`ai-n3`). An active duplicate (queued or running, same dedup key) is returned instead of a second row.
   */
  enqueue(nodeId: string, type: 'status.refresh', rawParams: unknown, actor: { userId: string | null }, source: CommandSource): Promise<EnqueuedCommand>;
}

/** `status.refresh`: no params, queued TTL 2 minutes, one active per node (spec section 3.7 policy table). */
const STATUS_REFRESH = { stored: 'localai.status.refresh', dedupKey: 'status.refresh:-', queuedTtlMs: 2 * 60_000 } as const;

const ACTIVE_STATUSES = ['queued', 'running'] as const;

@Injectable()
export class PrismaCommandEnqueuer implements LocalaiCommandEnqueuer {
  constructor(private readonly prisma: PrismaService) {}

  async enqueue(nodeId: string, type: 'status.refresh', rawParams: unknown, actor: { userId: string | null }, source: CommandSource): Promise<EnqueuedCommand> {
    if (type !== 'status.refresh') throw new Error(`Command type ${String(type)} is not supported by the interim enqueuer`);
    if (rawParams !== undefined && (typeof rawParams !== 'object' || rawParams === null || Object.keys(rawParams).length > 0)) {
      throw new Error('status.refresh takes no parameters');
    }
    const node = await this.prisma.localaiNode.findUnique({ where: { nodeId }, select: { id: true } });
    if (!node) throw new Error(`Unknown Local AI node ${nodeId}`);
    try {
      const row = await this.prisma.localaiCommand.create({
        data: {
          nodeId: node.id,
          type: STATUS_REFRESH.stored,
          params: {},
          dedupKey: STATUS_REFRESH.dedupKey,
          source,
          requestedById: actor.userId,
          expiresAt: new Date(Date.now() + STATUS_REFRESH.queuedTtlMs),
        },
        select: { id: true },
      });
      return { id: row.id, created: true };
    } catch (error) {
      if (!(error instanceof Prisma.PrismaClientKnownRequestError) || error.code !== 'P2002') throw error;
    }
    // The partial unique index refused the insert: an active one exists. Return it.
    const active = await this.prisma.localaiCommand.findFirst({
      where: { nodeId: node.id, dedupKey: STATUS_REFRESH.dedupKey, status: { in: [...ACTIVE_STATUSES] } },
      select: { id: true },
      orderBy: { createdAt: 'desc' },
    });
    if (!active) throw new Error('The active duplicate command vanished; try again');
    return { id: active.id, created: false };
  }
}
