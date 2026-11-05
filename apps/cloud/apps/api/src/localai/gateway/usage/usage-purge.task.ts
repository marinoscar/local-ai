// The usage purge's schedule (docs/specs/gateway.md section 4.5). THIS TASK DOES NO PURGING: it only decides the purge is
// due and enqueues the queue job `localai.gateway.usage_purge` (`usage-purge.job.ts`) through the platform's housekeeping
// helper, which skips the enqueue while one is already pending or running. Once a day, at a quiet minute of the server's clock (the retention is in whole days, so which timezone is
// irrelevant). The decorator takes no options object: the platform's `cron-enqueue-only` scan reads the first brace after
// the decorator as the method body.
import { Inject, Injectable, Logger } from '@nestjs/common';
import { Cron } from '@nestjs/schedule';
import { PLATFORM_PRISMA } from '@marinoscar/platform-api/core';
import { JobsService, enqueueHousekeepingJob, type JobsPrisma } from '@marinoscar/platform-api/jobs';

import { USAGE_PURGE_TYPE } from './usage-purge.job';

/** 03:21 every day. */
export const USAGE_PURGE_CRON = '21 3 * * *';

@Injectable()
export class UsagePurgeTask {
  private readonly logger = new Logger(UsagePurgeTask.name);

  constructor(
    private readonly jobs: JobsService,
    @Inject(PLATFORM_PRISMA) private readonly prisma: JobsPrisma,
  ) {}

  @Cron(USAGE_PURGE_CRON)
  async handleCron(): Promise<void> {
    await enqueueHousekeepingJob({
      jobs: this.jobs,
      prisma: this.prisma,
      logger: this.logger,
      type: USAGE_PURGE_TYPE,
      what: 'Gateway usage purge',
    });
  }
}
