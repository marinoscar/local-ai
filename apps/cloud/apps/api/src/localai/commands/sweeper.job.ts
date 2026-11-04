// The command sweeper's job (docs/specs/cloud-app.md section 4.4, "Sweeper"): the work that keeps the command table
// honest, run as a registered queue job so it is observable, retried and never done inline by a cron
// (docs/specs/job-queue.md, "All long-running work is a job"). `sweeper.task.ts` only decides it is due and enqueues
// it every 30 seconds.
//
//   queued  past expires_at           -> expired  (LAI-CLD-030)
//   running past lease_expires_at     -> failed   (LAI-CLD-059); the claim token is cleared, so a late result,
//                                        failure or renew from the node is a 409 (it never overtakes the sweeper)
//   logs.tail results older than 7 d  -> result = null (log lines are the only free text a result keeps)
//   commands older than 180 days      -> deleted (terminal ones only)
//
// SERVER-ONLY by design: it carries neither `nodeResultSchema` nor `persistNodeResult`, like every job that is a
// sequence of database statements. Every settle publishes `localai.command.settled` after the write committed.
// The job type string is permanent once a job of it exists.
import { Inject, Injectable, Logger, type OnModuleInit } from '@nestjs/common';
import { Prisma } from '@prisma/client';
import { JobHandlerRegistry, type JobHandler } from '@marinoscar/platform-api/jobs';

import { PrismaService } from '../../prisma/prisma.service';
import { LOCALAI_CLOCK, type LocalaiClock } from './command-events';
import { EXPIRED_ERROR, LEASE_LOST_ERROR } from './command-failure';
import { TERMINAL_STATUSES } from './command-lifecycle';
import { CommandsService } from './commands.service';

/** The queue type of the sweep; permanent. */
export const COMMANDS_SWEEP_TYPE = 'localai.commands.sweep';

/** Retention of a `logs.tail` result and of a command row, in days (spec 4.4). */
export const LOGS_RESULT_RETENTION_DAYS = 7;
export const COMMAND_RETENTION_DAYS = 180;

/** Rows settled per batch, and the most batches one run does (a backlog drains over the next runs). */
const BATCH = 200;
const MAX_ROUNDS = 10;
const DAY_MS = 24 * 60 * 60 * 1000;

export interface SweepSummary {
  expired: number;
  leaseLost: number;
  logsTrimmed: number;
  deleted: number;
}

@Injectable()
export class CommandsSweepHandler implements JobHandler, OnModuleInit {
  private readonly logger = new Logger(CommandsSweepHandler.name);

  readonly type = COMMANDS_SWEEP_TYPE;
  readonly label = 'Local AI command sweep';
  /** A sweep is a few indexed statements; the next cron tick enqueues another, so one attempt is enough. */
  readonly profile = { maxRuntimeMs: 60_000, maxAttempts: 1 } as const;

  constructor(
    private readonly registry: JobHandlerRegistry,
    private readonly prisma: PrismaService,
    private readonly commands: CommandsService,
    @Inject(LOCALAI_CLOCK) private readonly clock: LocalaiClock,
  ) {}

  /** Self-registration, the only queue wiring a handler needs. */
  onModuleInit(): void {
    this.registry.register(this);
  }

  async process(): Promise<void> {
    const summary = await this.sweep();
    if (summary.expired + summary.leaseLost + summary.logsTrimmed + summary.deleted > 0) {
      this.logger.log(
        `Command sweep: ${summary.expired} expired, ${summary.leaseLost} lost their lease, ` +
          `${summary.logsTrimmed} log results trimmed, ${summary.deleted} old commands deleted`,
      );
    }
  }

  /** One sweep at the clock's now. Each row is settled by its own conditional write, so a concurrent settle wins cleanly. */
  async sweep(): Promise<SweepSummary> {
    const now = this.clock.now();
    return {
      expired: await this.expireQueued(now),
      leaseLost: await this.failLostLeases(now),
      logsTrimmed: await this.trimLogResults(now),
      deleted: await this.deleteOld(now),
    };
  }

  private async expireQueued(now: Date): Promise<number> {
    let settled = 0;
    for (let round = 0; round < MAX_ROUNDS; round += 1) {
      const due = await this.prisma.localaiCommand.findMany({
        where: { status: 'queued', expiresAt: { lte: now } },
        select: { id: true, nodeId: true },
        orderBy: { expiresAt: 'asc' },
        take: BATCH,
      });
      for (const row of due) {
        const updated = await this.prisma.localaiCommand.updateMany({
          where: { id: row.id, status: 'queued', expiresAt: { lte: now } },
          data: { status: 'expired', error: EXPIRED_ERROR as unknown as Prisma.InputJsonValue, finishedAt: now },
        });
        if (updated.count === 1) {
          settled += 1;
          await this.commands.publishSettled({ nodeId: row.nodeId, commandId: row.id, status: 'expired' });
        }
      }
      if (due.length < BATCH) break;
    }
    return settled;
  }

  private async failLostLeases(now: Date): Promise<number> {
    let settled = 0;
    for (let round = 0; round < MAX_ROUNDS; round += 1) {
      const due = await this.prisma.localaiCommand.findMany({
        where: { status: 'running', leaseExpiresAt: { lte: now } },
        select: { id: true, nodeId: true },
        orderBy: { leaseExpiresAt: 'asc' },
        take: BATCH,
      });
      for (const row of due) {
        const updated = await this.prisma.localaiCommand.updateMany({
          where: { id: row.id, status: 'running', leaseExpiresAt: { lte: now } },
          data: {
            status: 'failed',
            error: LEASE_LOST_ERROR as unknown as Prisma.InputJsonValue,
            finishedAt: now,
            leaseExpiresAt: null,
            claimToken: null,
          },
        });
        if (updated.count === 1) {
          settled += 1;
          await this.commands.publishSettled({ nodeId: row.nodeId, commandId: row.id, status: 'failed' });
        }
      }
      if (due.length < BATCH) break;
    }
    return settled;
  }

  private async trimLogResults(now: Date): Promise<number> {
    const cutoff = new Date(now.getTime() - LOGS_RESULT_RETENTION_DAYS * DAY_MS);
    const trimmed = await this.prisma.localaiCommand.updateMany({
      where: { type: 'localai.logs.tail', status: 'succeeded', finishedAt: { lt: cutoff }, result: { not: Prisma.DbNull } },
      data: { result: Prisma.DbNull },
    });
    return trimmed.count;
  }

  private async deleteOld(now: Date): Promise<number> {
    const cutoff = new Date(now.getTime() - COMMAND_RETENTION_DAYS * DAY_MS);
    const deleted = await this.prisma.localaiCommand.deleteMany({
      where: { createdAt: { lt: cutoff }, status: { in: [...TERMINAL_STATUSES] } },
    });
    return deleted.count;
  }
}
