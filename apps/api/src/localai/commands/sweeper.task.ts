// The command sweeper's schedule (docs/specs/cloud-app.md section 4.4). THIS TASK DOES NO SWEEPING: it only decides
// the sweep is due and enqueues the queue job `localai.commands.sweep` (`sweeper.job.ts`) through the platform's
// housekeeping helper, which skips the enqueue while one is already pending or running. Every 30 seconds, so a
// command that expired or lost its lease is settled within about a minute (the Doctor flags a queued command past
// its expiry that is still not swept after 5 minutes, LAI-CLD-030).
import { Inject, Injectable, Logger } from '@nestjs/common';
import { Cron, CronExpression } from '@nestjs/schedule';
import { PLATFORM_PRISMA } from '@marinoscar/platform-api/core';
import { JobsService, enqueueHousekeepingJob, type JobsPrisma } from '@marinoscar/platform-api/jobs';

import { COMMANDS_SWEEP_TYPE } from './sweeper.job';

/**
 * The helper logs "Queued ..." at info on every enqueue; at one per 30 seconds that is 2880 lines a day of nothing
 * to read. Its success line goes to debug here; its warnings and errors (a sweep still pending, an enqueue that
 * failed) stay where they are.
 */
class SweepLogger extends Logger {
  override log(message: unknown, ...rest: unknown[]): void {
    this.debug(message, ...(rest as []));
  }
}

@Injectable()
export class CommandsSweepTask {
  private readonly logger = new SweepLogger(CommandsSweepTask.name);

  constructor(
    private readonly jobs: JobsService,
    @Inject(PLATFORM_PRISMA) private readonly prisma: JobsPrisma,
  ) {}

  @Cron(CronExpression.EVERY_30_SECONDS)
  async handleCron(): Promise<void> {
    await enqueueHousekeepingJob({
      jobs: this.jobs,
      prisma: this.prisma,
      logger: this.logger,
      type: COMMANDS_SWEEP_TYPE,
      what: 'Local AI command sweep',
    });
  }
}
