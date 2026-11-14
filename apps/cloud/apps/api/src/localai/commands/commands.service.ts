// The command plane's core (docs/specs/cloud-app.md section 4.4): enqueue a command for a node, and settle it.
// `enqueue` is the ONE entry the cloud's own code uses to queue a command; CLOUD-09 wraps it with the permission
// matrix, the node-state refusals and the audit rows. `renew`, `complete` and `fail` serve the node routes.
//
// Every state change is ONE conditional write: the `WHERE` carries the status and the held-claim predicate, so a
// sweeper that failed a command a moment ago, a duplicate post and a second node all lose cleanly (0 rows -> 409)
// rather than overwrite. Time comes from the injected clock (the cloud's time); events are published AFTER the write
// committed, and a failed publish is logged, never raised: the long poll also wakes on its own timeout.
import { Inject, Injectable, Logger, NotFoundException } from '@nestjs/common';
import { Prisma, type LocalaiCommand } from '@prisma/client';
import { AUDIT_SINK, type AuditEventInput, type AuditSink } from '@marinoscar/platform-api/core';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';

import { COMMAND_TYPE_PREFIX, COMMANDS, isCommandType, type CommandType } from '../contracts/commands';
import type { FailureRequest, RenewRequest, ResultRequest, SettleResponse } from '../contracts/command-wire';
import { PrismaService } from '../../prisma/prisma.service';
import { LOCALAI_PERMISSION_IDS } from '../localai.permissions';
import type { CommandSource, EnqueuedCommand, LocalaiCommandEnqueuer } from './command-enqueuer';
import { AUDIT_COMMAND_CANCELLED, AUDIT_COMMAND_REQUESTED, AUDIT_COMMAND_TARGET } from './command-audit';
import {
  commandForbidden,
  commandNotCancellable,
  featureOnlyCommand,
  invalidCommandParams,
  invalidResult,
  leaseLost,
  resultTypeMismatch,
  unknownCommand,
  unknownLabNode,
} from './command-errors';
import {
  COMMAND_ENQUEUED_CHANNEL,
  COMMAND_SETTLED_CHANNEL,
  LOCALAI_CLOCK,
  type CommandEnqueuedEvent,
  type CommandSettledEvent,
  type LocalaiClock,
} from './command-events';
import { parseFailureString } from './command-failure';
import { ACTIVE_STATUSES, isHeld, renewedLeaseExpiry } from './command-lifecycle';
import { assertModelOnNode, assertNodeTakesCommands, modelRequiredOnNode } from './command-refusals';
import { dedupKeyFor, FEATURE_ONLY_TYPES, maySend, permissionFor, policyOf, type CommandRequester } from './command-policy';
import { scrubJson } from './command-scrub';
import { redactParams, toCommandView, type CommandView } from './command-view';

/** Who asked: a person, or the system itself (a cron, a resend-keys fan-out). */
export interface CommandActor {
  userId: string | null;
}

export interface EnqueueResult {
  command: LocalaiCommand;
  /** False when an active command with the same dedup key already existed and was returned instead (HTTP 200, not 201). */
  created: boolean;
}

/** What `request` answers: the command as the UI sees it, and whether it is new (HTTP 201) or an active duplicate (200). */
export interface RequestResult {
  command: CommandView;
  created: boolean;
}

/** How many times `enqueue` retries when the active row that blocked its insert settled before it could be read. */
const ENQUEUE_MAX_ATTEMPTS = 3;

/** The dedup index, by the names Prisma reports for a violation of it (driver adapter or classic engine). */
const DEDUP_INDEX = 'localai_commands_active_dedup';

/** Whether `error` is a unique violation of the active-dedup index, and not some other P2002. */
export function isCommandDedupConflict(error: unknown): boolean {
  if (!(error instanceof Prisma.PrismaClientKnownRequestError) || error.code !== 'P2002') return false;
  const meta = (error.meta ?? {}) as Record<string, unknown>;
  const names = new Set([DEDUP_INDEX, 'dedup_key', 'dedupKey']);
  const cause = (meta.driverAdapterError as { cause?: { constraint?: { fields?: unknown; index?: unknown }; originalMessage?: unknown } } | undefined)?.cause;
  if (cause) {
    const fields = cause.constraint?.fields;
    if (Array.isArray(fields) && fields.some((field) => names.has(String(field)))) return true;
    if (cause.constraint?.index === DEDUP_INDEX) return true;
    if (typeof cause.originalMessage === 'string' && cause.originalMessage.includes(DEDUP_INDEX)) return true;
  }
  const target = meta.target;
  if (typeof target === 'string') return names.has(target);
  if (Array.isArray(target)) return target.some((entry) => names.has(String(entry)));
  return false;
}

@Injectable()
export class CommandsService implements LocalaiCommandEnqueuer {
  private readonly logger = new Logger(CommandsService.name);

  constructor(
    private readonly prisma: PrismaService,
    @Inject(EVENT_BUS) private readonly bus: EventBus,
    @Inject(LOCALAI_CLOCK) private readonly clock: LocalaiClock,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
  ) {}

  /**
   * The UI-facing entry (docs/specs/cloud-app.md sections 3.9 and 4.4, CLOUD-09): what a person or a script asks for.
   * In order: the type is a registry type (400); the caller holds the permission the policy table names for it
   * (403); the params pass the type's strict schema (400); the node exists (404), is enrolled (409 LAI-CLD-035) and
   * is not in Offline or Survival mode per its last snapshot (422 LAI-CLD-034); a model command finds the model
   * downloaded on the node (422). Then `enqueueForNode` does the insert and the dedup. A NEW command is audited
   * (`localai.command.requested`); an active duplicate returned with `created: false` writes nothing.
   * `type` may be unprefixed (`model.load`); the answer carries the prefixed one.
   */
  async request(nodeId: string, type: string, rawParams: unknown, requester: CommandRequester, source: CommandSource): Promise<RequestResult> {
    const full = this.resolveType(type);
    if (FEATURE_ONLY_TYPES.has(full)) throw featureOnlyCommand(full);
    if (!maySend(full, requester.permissions)) throw commandForbidden(permissionFor(full));
    const parsed = COMMANDS[full].params.safeParse(rawParams ?? {});
    if (!parsed.success) throw invalidCommandParams(parsed.error, full);

    const node = await this.prisma.localaiNode.findUnique({ where: { nodeId }, select: { id: true, nodeId: true, phase: true, mode: true } });
    if (!node) throw unknownLabNode();
    assertNodeTakesCommands(node);
    const modelId = modelRequiredOnNode(full, parsed.data);
    if (modelId !== null) {
      const model = await this.prisma.localaiNodeModel.findUnique({ where: { nodeId_modelId: { nodeId: node.id, modelId } }, select: { present: true } });
      assertModelOnNode(model);
    }

    const { command, created } = await this.enqueueForNode(node.id, full, parsed.data, { userId: requester.userId }, source);
    if (created) {
      await this.recordAudit({
        action: AUDIT_COMMAND_REQUESTED,
        actorUserId: requester.userId,
        targetType: AUDIT_COMMAND_TARGET,
        targetId: command.id,
        meta: { nodeId: node.nodeId, type: full, params: JSON.stringify(redactParams(full, command.params)) },
      });
    }
    return { command: toCommandView(command, node.nodeId), created };
  }

  /**
   * Cancels a `queued` command (DELETE /commands/:id). The caller needs the permission of the command's type; an
   * unknown or non-uuid id is 404. One conditional write (`WHERE status = 'queued'`): a command a node claimed in the
   * meantime, or one that already settled, is 409. Leaving `queued` frees the command's dedup key. Audited as
   * `localai.command.cancelled`.
   */
  async cancel(commandId: string, requester: CommandRequester): Promise<CommandView> {
    const row = await this.prisma.localaiCommand.findUnique({ where: { id: commandId }, include: { node: { select: { nodeId: true } } } });
    if (!row) throw unknownCommand();
    // A stored type this build does not know (removed in a later release) is treated as an admin type.
    const needed = isCommandType(row.type) ? permissionFor(row.type) : LOCALAI_PERMISSION_IDS.LAB_ADMIN;
    if (!requester.permissions.includes(needed)) throw commandForbidden(needed);

    const now = this.clock.now();
    const updated = await this.prisma.localaiCommand.updateMany({
      where: { id: commandId, status: 'queued' },
      data: { status: 'cancelled', finishedAt: now },
    });
    if (updated.count !== 1) {
      const current = await this.prisma.localaiCommand.findUnique({ where: { id: commandId }, select: { status: true } });
      if (!current) throw unknownCommand();
      throw commandNotCancellable(current.status);
    }

    await this.recordAudit({
      action: AUDIT_COMMAND_CANCELLED,
      actorUserId: requester.userId,
      targetType: AUDIT_COMMAND_TARGET,
      targetId: commandId,
      meta: { nodeId: row.node.nodeId, type: row.type, params: JSON.stringify(redactParams(row.type, row.params)) },
    });
    return toCommandView({ ...row, status: 'cancelled', finishedAt: now }, row.node.nodeId);
  }

  /** The registry type for a body `type` (with or without the `localai.` prefix); anything else is a 400. */
  private resolveType(type: string): CommandType {
    const full = type.startsWith(COMMAND_TYPE_PREFIX) ? type : `${COMMAND_TYPE_PREFIX}${type}`;
    if (!isCommandType(full)) throw invalidCommandParams(null, type);
    return full;
  }

  /** Audit runs after the write it records; a failure is logged (the error name only) and never undoes the write. */
  private async recordAudit(event: AuditEventInput): Promise<void> {
    try {
      await this.audit.record(event);
    } catch (error) {
      this.logger.error(`audit ${event.action} failed for command_id=${event.targetId} (${error instanceof Error ? error.name : 'error'})`);
    }
  }

  /**
   * The port every other feature queues through (`LOCALAI_COMMAND_ENQUEUER`): `nodeId` is the lab id (`ai-n3`) and
   * `type` the name without the prefix (a prefixed name is accepted too). Returns the command's id and whether it was
   * created (`false`: an active duplicate was returned). Permission checks and node-state refusals are CLOUD-09's.
   */
  async enqueue(nodeId: string, type: string, rawParams: unknown, actor: CommandActor, source: CommandSource): Promise<EnqueuedCommand> {
    const node = await this.prisma.localaiNode.findUnique({ where: { nodeId }, select: { id: true } });
    if (!node) throw new NotFoundException({ message: 'No Local AI node has this id', details: { reason: 'unknown_localai_node' } });
    const full = type.startsWith(COMMAND_TYPE_PREFIX) ? type : `${COMMAND_TYPE_PREFIX}${type}`;
    const { command, created } = await this.enqueueForNode(node.id, full, rawParams, actor, source);
    return { id: command.id, created };
  }

  /**
   * Queues a command for a node by its row id (`localai_nodes.id`). `type` is the full name (`localai.model.load`);
   * `rawParams` is validated by the type's strict schema (a failure is a 400 with the Zod issues). If an active
   * command with the same dedup key exists, it is returned with `created: false`.
   */
  async enqueueForNode(nodeId: string, type: string, rawParams: unknown, actor: CommandActor, source: CommandSource): Promise<EnqueueResult> {
    if (!isCommandType(type)) throw invalidCommandParams(null, type);
    const parsed = COMMANDS[type].params.safeParse(rawParams ?? {});
    if (!parsed.success) throw invalidCommandParams(parsed.error, type);

    const params = parsed.data as Record<string, unknown>;
    const dedupKey = dedupKeyFor(type, params);
    const now = this.clock.now();
    const data: Prisma.LocalaiCommandUncheckedCreateInput = {
      nodeId,
      type,
      params: params as Prisma.InputJsonValue,
      status: 'queued',
      dedupKey,
      source,
      requestedById: actor.userId,
      expiresAt: new Date(now.getTime() + policyOf(type).queuedTtlMs),
    };

    let lastConflict: unknown;
    for (let attempt = 1; attempt <= ENQUEUE_MAX_ATTEMPTS; attempt += 1) {
      try {
        const command = await this.prisma.localaiCommand.create({ data });
        await this.publishEnqueued(nodeId);
        return { command, created: true };
      } catch (error) {
        if (!isCommandDedupConflict(error)) throw error;
        lastConflict = error;
        const active = await this.prisma.localaiCommand.findFirst({
          where: { nodeId, dedupKey, status: { in: [...ACTIVE_STATUSES] } },
          orderBy: { createdAt: 'asc' },
        });
        if (active) return { command: active, created: false };
        // The blocking row settled between the insert and the read: the key is free again, try once more.
      }
    }
    throw lastConflict;
  }

  /**
   * Extends the lease of a command this node holds. Returns the new lease expiry, or null when the claim is not
   * held (wrong token or node, not running, lease passed) or the hard cap of the type has been reached; the caller
   * answers 409 for null. The write repeats the held-claim predicate, so a sweep that lands in between wins.
   */
  async renew(nodeId: string, commandId: string, body: RenewRequest): Promise<Date | null> {
    const now = this.clock.now();
    const row = await this.prisma.localaiCommand.findFirst({ where: { id: commandId, nodeId } });
    if (!row || row.status !== 'running' || row.claimToken !== body.claimToken || !row.claimedAt || !isCommandType(row.type)) return null;

    const next = renewedLeaseExpiry(row.type, row.claimedAt, now);
    if (!next) return null;
    const updated = await this.prisma.localaiCommand.updateMany({
      where: { id: commandId, nodeId, status: 'running', claimToken: body.claimToken, leaseExpiresAt: { gt: now }, claimedAt: row.claimedAt },
      data: { leaseExpiresAt: next },
    });
    return updated.count === 1 ? next : null;
  }

  /**
   * Settles a held command as `succeeded`. 409 when the claim is not held; 400 when the result's type differs from
   * the command's or the result fails the type's schema (the command then stays `running`). A repeat of the very
   * post that succeeded (same token) answers `succeeded` again and changes nothing.
   */
  async complete(nodeId: string, commandId: string, body: ResultRequest): Promise<SettleResponse> {
    const now = this.clock.now();
    const row = await this.prisma.localaiCommand.findFirst({ where: { id: commandId, nodeId } });
    if (!row) throw leaseLost();
    if (row.status === 'succeeded' && row.claimToken === body.claimToken) return { status: 'succeeded' };
    if (!isHeld(row, nodeId, body.claimToken, now) || !isCommandType(row.type)) throw leaseLost();

    if (body.type !== row.type) throw resultTypeMismatch();
    const type: CommandType = row.type;
    const parsed = COMMANDS[type].result.safeParse(body.result);
    if (!parsed.success) throw invalidResult(parsed.error);
    // `logs.tail` lines are scrubbed again here: a log line is the one free-text field a result carries.
    const result = type === 'localai.logs.tail' ? scrubJson(parsed.data) : parsed.data;

    const updated = await this.prisma.localaiCommand.updateMany({
      where: { id: commandId, nodeId, status: 'running', claimToken: body.claimToken, leaseExpiresAt: { gt: now } },
      data: { status: 'succeeded', result: result as Prisma.InputJsonValue, finishedAt: now, leaseExpiresAt: null },
    });
    if (updated.count !== 1) throw leaseLost();

    if (type === 'localai.mode.offline') await this.markNodeOffline(nodeId);
    await this.publishSettled({ nodeId, commandId, status: 'succeeded' });
    return { status: 'succeeded' };
  }

  /** Settles a held command as `failed` with the agent's `"<code>: <message>"` string split and scrubbed. */
  async fail(nodeId: string, commandId: string, body: FailureRequest): Promise<SettleResponse> {
    const now = this.clock.now();
    const row = await this.prisma.localaiCommand.findFirst({ where: { id: commandId, nodeId } });
    if (!row) throw leaseLost();
    if (row.status === 'failed' && row.claimToken === body.claimToken) return { status: 'failed' };
    if (!isHeld(row, nodeId, body.claimToken, now)) throw leaseLost();

    const error = parseFailureString(body.error);
    const updated = await this.prisma.localaiCommand.updateMany({
      where: { id: commandId, nodeId, status: 'running', claimToken: body.claimToken, leaseExpiresAt: { gt: now } },
      data: { status: 'failed', error: error as unknown as Prisma.InputJsonValue, finishedAt: now, leaseExpiresAt: null },
    });
    if (updated.count !== 1) throw leaseLost();

    await this.publishSettled({ nodeId, commandId, status: 'failed' });
    return { status: 'failed' };
  }

  /** `mode.offline` side effect: the node reported it, so the cloud shows it at once instead of at the next snapshot. */
  private async markNodeOffline(nodeId: string): Promise<void> {
    try {
      await this.prisma.localaiNode.update({ where: { id: nodeId }, data: { mode: 'offline' } });
    } catch (error) {
      // The result is stored; the next snapshot carries the truth. Log the error's name only.
      this.logger.warn(`Could not record the offline mode of node ${nodeId} (${error instanceof Error ? error.name : 'error'})`);
    }
  }

  /** Publishes `localai.command.enqueued`. */
  async publishEnqueued(nodeId: string): Promise<void> {
    await this.publish<CommandEnqueuedEvent>(COMMAND_ENQUEUED_CHANNEL, { nodeId });
  }

  /** Publishes `localai.command.settled` after a result, a failure or the sweeper. */
  async publishSettled(event: CommandSettledEvent): Promise<void> {
    await this.publish<CommandSettledEvent>(COMMAND_SETTLED_CHANNEL, event);
  }

  private async publish<T>(channel: string, payload: T): Promise<void> {
    try {
      await this.bus.publish(channel, payload);
    } catch (error) {
      this.logger.warn(`Could not publish ${channel} (${error instanceof Error ? error.name : 'error'})`);
    }
  }
}
