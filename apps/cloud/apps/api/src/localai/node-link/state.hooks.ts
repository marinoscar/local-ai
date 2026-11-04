// The two seams of state ingestion that later issues fill (docs/specs/cloud-app.md section 4.3, step 5).
//
// ALERT RE-EVALUATION. When a node's mode changes, its alerts are re-evaluated at once instead of at the next sweep
// (an expected silence for an offline-mode node must stop firing immediately). The alert engine is CLOUD-15, which is
// not built yet; until then this port has a no-op default, so ingestion already calls it at the right moment (after
// the snapshot's transaction committed) and CLOUD-15 only has to bind a real implementation to
// `LOCALAI_ALERT_REEVALUATOR` in `NodeLinkModule`. It must never throw into the request; the caller also catches.
import { Injectable, Logger } from '@nestjs/common';

/** Injection token of the {@link LocalaiAlertReevaluator}. */
export const LOCALAI_ALERT_REEVALUATOR = Symbol('LOCALAI_ALERT_REEVALUATOR');

export interface LocalaiAlertReevaluator {
  /** Re-evaluate the alerts of one node after its mode changed. `nodeId` is the lab node id, `ai-n<N>`. */
  reevaluateNode(nodeId: string): Promise<void>;
}

/** The default until CLOUD-15 lands: it only leaves a log line (ids only). */
@Injectable()
export class NoopLocalaiAlertReevaluator implements LocalaiAlertReevaluator {
  private readonly logger = new Logger(NoopLocalaiAlertReevaluator.name);

  async reevaluateNode(nodeId: string): Promise<void> {
    this.logger.debug(`alert re-evaluation requested: node_id=${nodeId} (no alert engine registered yet)`);
  }
}
