// `localai.alerts.evaluate`: the alert evaluation as a registered queue job (docs/specs/cloud-app.md section 4.9;
// docs/specs/job-queue.md, "All long-running work is a job"). Two ways in:
//   - no payload       every non-retired node and the lab-wide rules (what `alerts.cron.ts` enqueues every 5 minutes);
//   - `{ nodeId }`     one node, queued by the state ingestion when that node's mode changed (`alert-reevaluator.ts`), so an
//                      expected silence stops firing at once instead of at the next tick.
//
// SERVER-ONLY by design: it carries neither `nodeResultSchema` nor `persistNodeResult` (it reads several tables and
// writes as it goes), so it is never offered to a worker node. One attempt: the next tick is the retry. The job type
// string is permanent once a job of it exists.
import { Injectable, Logger, type OnModuleInit } from '@nestjs/common';
import { JobHandlerRegistry, type Job, type JobHandler } from '@marinoscar/platform-api/jobs';
import { z } from 'zod';

import { AlertsService } from './alerts.service';

/** The queue type of the evaluation; permanent. */
export const ALERTS_EVALUATE_TYPE = 'localai.alerts.evaluate';

/** The `subjectType` of a single-node evaluation (the subject id is the lab node id). */
export const ALERTS_NODE_SUBJECT = 'localai_node';

const PayloadSchema = z.object({ nodeId: z.string().min(1).max(64).optional() }).strict();

@Injectable()
export class AlertsEvaluateHandler implements JobHandler, OnModuleInit {
  private readonly logger = new Logger(AlertsEvaluateHandler.name);

  readonly type = ALERTS_EVALUATE_TYPE;
  readonly label = 'Local AI alert evaluation';
  /** A few indexed statements per node; the next cron tick enqueues another, so one attempt is enough. */
  readonly profile = { maxRuntimeMs: 120_000, maxAttempts: 1 } as const;

  constructor(
    private readonly registry: JobHandlerRegistry,
    private readonly alerts: AlertsService,
  ) {}

  onModuleInit(): void {
    this.registry.register(this);
  }

  async process(job: Job): Promise<void> {
    const payload = PayloadSchema.parse(job.payload ?? {});
    const summary = payload.nodeId !== undefined ? await this.alerts.evaluateNode(payload.nodeId) : await this.alerts.evaluateAll();
    if (summary.raised + summary.escalated + summary.cleared + summary.notified > 0) {
      this.logger.log(
        `Alert evaluation${payload.nodeId ? ` of ${payload.nodeId}` : ''}: ${summary.raised} raised, ${summary.escalated} escalated, ` +
          `${summary.cleared} cleared, ${summary.notified} notifications`,
      );
    }
  }
}
