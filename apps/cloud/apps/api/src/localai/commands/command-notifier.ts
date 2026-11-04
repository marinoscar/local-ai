// The hook that raises the `localai.command.failed` notification (docs/specs/cloud-app.md section 4.9): a browser
// notification to the person who sent the command, "{type} on {node} failed: {fix}".
//
// The notification is registered in `../localai.notifications.ts` as `localai.command_failed` (the platform registry takes
// one dot, so the spec's `localai.command.failed` became that permanent key) and `CommandsModule` binds
// `NotificationsCommandNotifier` (`command-failed.notifier.ts`) to `LOCALAI_COMMAND_NOTIFIER`. The no-op below stays as
// the default for a test or an app without the notifications slice. It must never throw into the listener, which also
// catches: a failed notification must not undo or hide the audit row.
import { Injectable, Logger } from '@nestjs/common';

/** Injection token of the {@link LocalaiCommandNotifier}. */
export const LOCALAI_COMMAND_NOTIFIER = Symbol('LOCALAI_COMMAND_NOTIFIER');

/** The spec's name of the event (section 4.9); the registered notification key is `localai.command_failed`. Used in log lines only. */
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

/** A notifier that only leaves a log line (ids and codes, no text). */
@Injectable()
export class NoopLocalaiCommandNotifier implements LocalaiCommandNotifier {
  private readonly logger = new Logger(NoopLocalaiCommandNotifier.name);

  async commandFailed(event: LocalaiCommandFailedEvent): Promise<void> {
    this.logger.log(`${LOCALAI_COMMAND_FAILED_EVENT}: command_id=${event.commandId} node_id=${event.nodeId} code=${event.errorCode ?? '-'} (no notifier bound)`);
  }
}
