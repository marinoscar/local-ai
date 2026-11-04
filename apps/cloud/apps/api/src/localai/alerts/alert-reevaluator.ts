// The alert engine's side of `LOCALAI_ALERT_REEVALUATOR` (`node-link/state.hooks.ts`): when a node's mode changes, the
// state ingestion calls this after the snapshot committed. It does NOT evaluate inline (that would be work done in a
// request): it queues `localai.alerts.evaluate` for that one node and returns. `skipDedup` because two mode changes of
// one node are two facts; an evaluation already running may have read the node before the second one.
import { Injectable } from '@nestjs/common';
import { JobsService } from '@marinoscar/platform-api/jobs';

import type { LocalaiAlertReevaluator } from '../node-link/state.hooks';
import { ALERTS_EVALUATE_TYPE, ALERTS_NODE_SUBJECT } from './alerts-evaluate.job';

@Injectable()
export class JobsAlertReevaluator implements LocalaiAlertReevaluator {
  constructor(private readonly jobs: JobsService) {}

  async reevaluateNode(nodeId: string): Promise<void> {
    await this.jobs.enqueue({
      type: ALERTS_EVALUATE_TYPE,
      reason: 'rerun',
      subjectType: ALERTS_NODE_SUBJECT,
      subjectId: nodeId,
      payload: { nodeId },
      orgId: null,
      skipDedup: true,
    });
  }
}
