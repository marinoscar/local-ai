// The alert evaluation's schedule (docs/specs/cloud-app.md section 4.9). THIS TASK DOES NO EVALUATING: it only decides
// the evaluation is due and enqueues the queue job `localai.alerts.evaluate` (`alerts-evaluate.job.ts`) through the
// platform's housekeeping helper, which skips the enqueue while one is already pending or running. Every 5 minutes.
import { Inject, Injectable, Logger } from '@nestjs/common';
import { Cron } from '@nestjs/schedule';
import { PLATFORM_PRISMA } from '@marinoscar/platform-api/core';
import { JobsService, enqueueHousekeepingJob, type JobsPrisma } from '@marinoscar/platform-api/jobs';

import { ALERTS_EVALUATE_TYPE } from './alerts-evaluate.job';

/** The helper logs "Queued ..." at info on every enqueue; at one per 5 minutes that is noise, so it goes to debug. */
class EvaluateLogger extends Logger {
  override log(message: unknown, ...rest: unknown[]): void {
    this.debug(message, ...(rest as []));
  }
}

@Injectable()
export class AlertsEvaluateTask {
  private readonly logger = new EvaluateLogger(AlertsEvaluateTask.name);

  constructor(
    private readonly jobs: JobsService,
    @Inject(PLATFORM_PRISMA) private readonly prisma: JobsPrisma,
  ) {}

  @Cron('*/5 * * * *')
  async handleCron(): Promise<void> {
    await enqueueHousekeepingJob({
      jobs: this.jobs,
      prisma: this.prisma,
      logger: this.logger,
      type: ALERTS_EVALUATE_TYPE,
      what: 'Local AI alert evaluation',
    });
  }
}
