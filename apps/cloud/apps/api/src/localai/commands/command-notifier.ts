// The hook that raises the `localai.command.failed` notification (docs/specs/cloud-app.md section 4.9): a browser
// notification to the person who sent the command, "{type} on {node} failed: {fix}".
//
// The notification registration (event key, channels, default on) belongs to CLOUD-15, which is not built yet. Until
// then this port has a no-op default, so the settled-command listener already calls it at the right moment (after
// the settling write committed, outside any transaction) and CLOUD-15 only has to bind a real implementation to
// `LOCALAI_COMMAND_NOTIFIER`. It must never throw into the listener, which also catches: a failed notification must
// not undo or hide the audit row.
import { Injectable, Logger } from '@nestjs/common';

/** Injection token of the {@link LocalaiCommandNotifier}. */
export const LOCALAI_COMMAND_NOTIFIER = Symbol('LOCALAI_COMMAND_NOTIFIER');

/** The permanent event key of the notification (spec section 4.9). */
export const LOCALAI_COMMAND_FAILED_EVENT = 'localai.command.failed';

export interface LocalaiCommandFailedEvent {
  /** The person who sent the command (`localai_commands.requested_by_id`); the only recipient. */
  recipientUserId: string;
  commandId: string;
  /** The lab node id, `ai-n<N>`. */
  nodeId: string;
  /** The node's display name, for the message. */
  nodeName: string;
  /** The type without the prefix (`model.load`). */
  type: string;
  /** `failed` (the agent or the sweeper) or `expired` (nobody claimed it in time). */
  status: 'failed' | 'expired';
  /** The doctor code stored with the failure, when there is one. */
  errorCode: string | null;
  /** The plain-language fix of the code. */
  fix: string;
  /** The rendered body: "{type} on {node} failed: {fix}". */
  message: string;
}

export interface LocalaiCommandNotifier {
  /** Called once per failed or expired command that has a requester, after the settling write committed. */
  commandFailed(event: LocalaiCommandFailedEvent): Promise<void>;
}

/** The default until CLOUD-15 registers the notification: it only leaves a log line (ids and codes, no text). */
@Injectable()
export class NoopLocalaiCommandNotifier implements LocalaiCommandNotifier {
  private readonly logger = new Logger(NoopLocalaiCommandNotifier.name);

  async commandFailed(event: LocalaiCommandFailedEvent): Promise<void> {
    this.logger.log(`${LOCALAI_COMMAND_FAILED_EVENT}: command_id=${event.commandId} node_id=${event.nodeId} code=${event.errorCode ?? '-'} (no notification registered yet)`);
  }
}
