// The daily purge of the per-request usage rows (docs/specs/gateway.md section 4.5), as a registered queue job so it is
// observable and retried and never runs inline in a cron (`usage-purge.task.ts` only enqueues it). It deletes the rows
// of `localai_gateway_usage` older than `gateway.usageRetentionDays` (90); the daily rollups
// (`localai_gateway_usage_daily`) are kept forever. Rows go in batches of 5000 ids (an index range scan on `created_at`
// then a delete by id), so no statement holds a long lock; a backlog drains over the next runs.
//
// SERVER-ONLY: neither `nodeResultSchema` nor `persistNodeResult`, like every job that is a few database statements.
// The job type string is permanent once a job of it exists. It has no organization (`orgId: null`, a housekeeping job).
import { Injectable, Logger, type OnModuleInit } from '@nestjs/common';
import { JobHandlerRegistry, type JobHandler } from '@marinoscar/platform-api/jobs';

import { PrismaService } from '../../../prisma/prisma.service';
import { LocalaiSettingsService } from '../../settings.service';
import { DAY_MS } from './usage-day';

/** The queue type of the purge; permanent. */
export const USAGE_PURGE_TYPE = 'localai.gateway.usage_purge';

const BATCH = 5000;
const MAX_ROUNDS = 200;

@Injectable()
export class UsagePurgeHandler implements JobHandler, OnModuleInit {
  private readonly logger = new Logger(UsagePurgeHandler.name);

  readonly type = USAGE_PURGE_TYPE;
  readonly label = 'Gateway usage purge';
  /** A purge is indexed deletes; tomorrow's run continues a backlog, so one attempt is enough. */
  readonly profile = { maxRuntimeMs: 600_000, maxAttempts: 1 } as const;

  constructor(
    private readonly registry: JobHandlerRegistry,
    private readonly prisma: PrismaService,
    private readonly settings: LocalaiSettingsService,
  ) {}

  /** The clock; a spec replaces it. */
  now: () => number = Date.now;

  /** Self-registration, the only queue wiring a handler needs. */
  onModuleInit(): void {
    this.registry.register(this);
  }

  async process(): Promise<void> {
    const deleted = await this.purge();
    if (deleted > 0) this.logger.log(`Gateway usage purge: ${deleted} request rows deleted`);
  }

  /** Deletes the rows older than the retention; answers how many. */
  async purge(): Promise<number> {
    const { gateway } = await this.settings.readAll();
    const cutoff = new Date(this.now() - gateway.usageRetentionDays * DAY_MS);
    let total = 0;
    for (let round = 0; round < MAX_ROUNDS; round += 1) {
      const ids = await this.prisma.localaiGatewayUsage.findMany({ where: { createdAt: { lt: cutoff } }, select: { id: true }, orderBy: { createdAt: 'asc' }, take: BATCH });
      if (ids.length === 0) break;
      const { count } = await this.prisma.localaiGatewayUsage.deleteMany({ where: { id: { in: ids.map((row) => row.id) } } });
      total += count;
      if (ids.length < BATCH) break;
    }
    return total;
  }
}
