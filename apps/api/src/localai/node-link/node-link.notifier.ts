// The hook that raises the `localai.node.enrolled` notification (docs/specs/cloud-app.md section 4.9).
//
// The notification registration (event key, channels, recipients: holders of `lab:admin`) belongs to CLOUD-15,
// which is not built yet. Until then this port has a no-op default, so the binding guard already calls it at the
// right moment (after the enrolling write committed, outside any transaction) and CLOUD-15 only has to bind a
// real implementation to `LOCALAI_NODE_NOTIFIER`. It must never throw into the guard's request: the guard also
// catches, because a failed notification must not refuse a node that has just enrolled.
import { Injectable, Logger } from '@nestjs/common';

/** Injection token of the {@link LocalaiNodeNotifier}. */
export const LOCALAI_NODE_NOTIFIER = Symbol('LOCALAI_NODE_NOTIFIER');

/** The permanent event key of the notification (spec section 4.9). */
export const LOCALAI_NODE_ENROLLED_EVENT = 'localai.node.enrolled';

export interface LocalaiNodeEnrolledEvent {
  /** The lab node id, `ai-n<N>`. */
  nodeId: string;
  /** The node's display name, for the message "{node} joined the lab". */
  displayName: string;
  /** The worker node (`worker_nodes.id`) it enrolled through. */
  workerNodeId: string;
}

export interface LocalaiNodeNotifier {
  /** Called once per node, after the `pending` to `enrolled` write committed. */
  nodeEnrolled(event: LocalaiNodeEnrolledEvent): Promise<void>;
}

/** The default until CLOUD-15 registers the notification: it only leaves a log line (no secrets, ids only). */
@Injectable()
export class NoopLocalaiNodeNotifier implements LocalaiNodeNotifier {
  private readonly logger = new Logger(NoopLocalaiNodeNotifier.name);

  async nodeEnrolled(event: LocalaiNodeEnrolledEvent): Promise<void> {
    this.logger.log(`${LOCALAI_NODE_ENROLLED_EVENT}: node_id=${event.nodeId} (no notification registered yet)`);
  }
}
